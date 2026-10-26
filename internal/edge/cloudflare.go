package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The client of the Cloudflare v4 API that the deploy of the Worker uses (deploy.go). It sends
// the token as a bearer and never puts it anywhere else: not in an error, a log line or the
// status. Cloudflare's own messages are redacted of it too, in case one echoes a header.

// cfAPI is the base of the Cloudflare v4 API.
const cfAPI = "https://api.cloudflare.com/client/v4"

// Per-call timeouts: the upload of the Worker sends its bundle, every other call is small.
const (
	cfCallTimeout   = 30 * time.Second
	cfUploadTimeout = 2 * time.Minute
	// cfMaxRetryAfter is the longest Retry-After of a 429 that a call waits for; a longer one
	// fails the call with the wait in its message.
	cfMaxRetryAfter = 2 * time.Minute
)

// cfHTTP sends the calls; it follows no redirect, since the API never sends one and a redirect
// would carry the bearer to another host.
var cfHTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// cfClient calls the Cloudflare API with one token.
type cfClient struct {
	base  string
	token string
}

// cfMessage is one entry of Cloudflare's errors (or of an error_chain under it).
type cfMessage struct {
	Code       int         `json:"code"`
	Message    string      `json:"message"`
	ErrorChain []cfMessage `json:"error_chain,omitempty"`
}

type cfEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []cfMessage     `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// cfError is a Cloudflare error code and message, as the status and the audit event show them.
type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callError is a failed call of the Cloudflare API: the method and the path (IDs and names, never
// the token), the HTTP status (0 when no answer came) and Cloudflare's errors with their chains
// flattened, or the transport error.
type callError struct {
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	Errors     []cfError `json:"errors"`
	Transport  string    `json:"transport,omitempty"`
	RetryAfter int       `json:"retry_after,omitempty"` // seconds of a 429 that the call did not wait for
}

func (e *callError) Error() string {
	var b strings.Builder
	b.WriteString(e.Method + " " + e.Path)
	switch {
	case e.Transport != "":
		b.WriteString(" got no answer: " + e.Transport)
		return b.String()
	case e.Status != 0:
		fmt.Fprintf(&b, " answered HTTP %d", e.Status)
	}
	if len(e.Errors) > 0 {
		b.WriteString(": ")
		for i, m := range e.Errors {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "Cloudflare error %d: %s", m.Code, m.Message)
		}
	}
	if e.RetryAfter > 0 {
		fmt.Fprintf(&b, " (Cloudflare asks to wait %d s)", e.RetryAfter)
	}
	return b.String()
}

// has reports whether Cloudflare answered one of codes.
func (e *callError) has(codes ...int) bool {
	for _, m := range e.Errors {
		for _, c := range codes {
			if m.Code == c {
				return true
			}
		}
	}
	return false
}

// asCall returns the callError in err, or nil.
func asCall(err error) *callError {
	var ce *callError
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

// cfRequest is one call.
type cfRequest struct {
	method string
	path   string // under cfAPI, with the IDs in it (each path segment escaped by the caller)
	query  url.Values
	header map[string]string
	body   []byte
	ctype  string // the Content-Type of body; "" with a body is application/json
	upload bool   // the longer timeout
}

// idempotent reports whether a call may be sent again after a server error or no answer: a
// POST that creates something may have created it.
func (r *cfRequest) idempotent() bool { return r.method != http.MethodPost }

// do sends req and decodes the result into out (nil to drop it). It waits out a 429 for its
// Retry-After (at most cfMaxRetryAfter, three times), and sends an idempotent call again twice
// after a 5xx or no answer. It returns the result_info of a page when there is one.
func (c *cfClient) do(ctx context.Context, req cfRequest, out any) (*cfEnvelope, error) {
	shown := req.method + " " + req.path
	var last error
	serverRetries, rateRetries := 0, 0
	for {
		env, wait, err := c.once(ctx, req)
		if err == nil {
			if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
				if err := json.Unmarshal(env.Result, out); err != nil {
					return nil, &callError{Method: req.method, Path: req.path, Status: http.StatusOK,
						Transport: "the answer does not parse: " + err.Error()}
				}
			}
			return env, nil
		}
		last = err
		ce := asCall(err)
		switch {
		case ce != nil && ce.Status == http.StatusTooManyRequests && rateRetries < 3 && wait <= cfMaxRetryAfter:
			rateRetries++
		case ce != nil && (ce.Status >= 500 || ce.Transport != "") && req.idempotent() && serverRetries < 2 && ctx.Err() == nil:
			serverRetries++
			wait = time.Duration(serverRetries) * 2 * time.Second
		default:
			return env, last
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: %w", shown, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// once sends req one time. wait is the Retry-After of a 429.
func (c *cfClient) once(ctx context.Context, req cfRequest) (*cfEnvelope, time.Duration, error) {
	timeout := cfCallTimeout
	if req.upload {
		timeout = cfUploadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := c.base + req.path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.method, u, body)
	if err != nil {
		return nil, 0, &callError{Method: req.method, Path: req.path, Transport: err.Error()}
	}
	hr.Header.Set("Authorization", "Bearer "+c.token)
	hr.Header.Set("Accept", "application/json")
	if req.body != nil {
		ct := req.ctype
		if ct == "" {
			ct = "application/json"
		}
		hr.Header.Set("Content-Type", ct)
	}
	for k, v := range req.header {
		hr.Header.Set(k, v)
	}
	resp, err := cfHTTP.Do(hr)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is in the path already
		}
		msg := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			msg = fmt.Sprintf("no answer within %s", timeout)
		}
		return nil, 0, &callError{Method: req.method, Path: req.path, Transport: c.redact(msg)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, &callError{Method: req.method, Path: req.path, Status: resp.StatusCode, Transport: "reading the answer: " + c.redact(err.Error())}
	}
	var env cfEnvelope
	parsed := json.Unmarshal(raw, &env) == nil
	if resp.StatusCode < 300 && parsed && env.Success {
		return &env, 0, nil
	}
	ce := &callError{Method: req.method, Path: req.path, Status: resp.StatusCode}
	if parsed {
		ce.Errors = c.flatten(env.Errors)
	}
	if len(ce.Errors) == 0 {
		text := strings.TrimSpace(string(raw))
		if len(text) > 300 {
			text = text[:300] + "..."
		}
		if text == "" {
			text = http.StatusText(resp.StatusCode)
		}
		ce.Errors = []cfError{{Message: c.redact(text)}}
	}
	var wait time.Duration
	if resp.StatusCode == http.StatusTooManyRequests {
		wait = retryAfter(resp.Header.Get("Retry-After"))
		if wait > cfMaxRetryAfter {
			ce.RetryAfter = int(wait / time.Second)
		}
	}
	if !parsed && resp.StatusCode < 300 {
		ce.Transport = "the answer is not the JSON of the Cloudflare API"
	}
	return &env, wait, ce
}

// flatten returns the errors with their error_chain entries after each, redacted.
func (c *cfClient) flatten(ms []cfMessage) []cfError {
	var out []cfError
	var walk func([]cfMessage)
	walk = func(ms []cfMessage) {
		for _, m := range ms {
			out = append(out, cfError{Code: m.Code, Message: c.redact(m.Message)})
			walk(m.ErrorChain)
		}
	}
	walk(ms)
	return out
}

// redact removes the token from s.
func (c *cfClient) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[the token]")
}

// retryAfter reads a Retry-After header: seconds or an HTTP date. A missing or bad one is 5 s.
func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(0, time.Until(t))
	}
	return 5 * time.Second
}

// get, post, put and del are the JSON calls.
func (c *cfClient) get(ctx context.Context, path string, q url.Values, hdr map[string]string, out any) (*cfEnvelope, error) {
	return c.do(ctx, cfRequest{method: http.MethodGet, path: path, query: q, header: hdr}, out)
}

func (c *cfClient) send(ctx context.Context, method, path string, hdr map[string]string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, cfRequest{method: method, path: path, header: hdr, body: b}, out)
	return err
}

// seg escapes one path segment.
func seg(s string) string { return url.PathEscape(s) }
