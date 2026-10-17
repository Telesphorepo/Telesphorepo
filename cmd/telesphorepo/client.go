package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"
)

// The texts that a device signs (the server's publish.SignedMessage, ChallengeMessage,
// SetupMessage and RevokeMessage).
const (
	signPrefix      = "telesphorepo publish v1\n"
	challengePrefix = "telesphorepo challenge v1\n"
	setupPrefix     = "telesphorepo setup v1\n"
	revokePrefix    = "telesphorepo revoke v1\n"
)

// versionHeader is the header that carries the version of the server on every answer.
const versionHeader = "Telesphorepo-Version"

// userAgent is the User-Agent of every request of the CLI and of its helper modes.
func userAgent() string {
	return "telesphorepo-cli/" + version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// client sends requests to one server, at its canonical URL only. It follows no redirect, so a
// credential never goes to another address.
type client struct {
	id   identity
	http *http.Client
	// onVersion gets the Telesphorepo-Version header of each answer. An error stops the request
	// that got the answer.
	onVersion func(ctx context.Context, v string) error
	// version is the last version header that the server sent ("" before the first answer).
	version string
}

func newClient(id identity) *client {
	return &client{id: id, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// request is one request of call.
type request struct {
	method, path string
	json         any       // a JSON body (nil: none)
	body         io.Reader // a body that is not JSON
	size         int64     // the length of body (-1: unknown)
	ctype        string    // the Content-Type of body
	token        string    // a publish session token for "Authorization: Bearer"
	header       map[string]string
	out          any           // decodes a JSON answer (nil: none)
	timeout      time.Duration // 0: 30 s; -1: none (an upload)
}

// call sends the request k and decodes its answer. An answer of 300 or more is a *problemError
// (a problem document, or the {"error"} of a format route) or an *httpError (any other body). A
// problem with the remedy retry is sent once more after its wait, when the body can be sent again.
func (c *client) call(ctx context.Context, k request) (http.Header, error) {
	h, err := c.callOnce(ctx, k)
	var pe *problemError
	if errors.As(err, &pe) && pe.p.Remedy != nil && pe.p.Remedy.Action == remedyRetry && k.body == nil {
		wait := time.Duration(min(max(pe.p.Remedy.After, 1), 60)) * time.Second
		fmt.Fprintf(os.Stderr, "%s Trying again in %s.\n", pe.p.Title, wait)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		return c.callOnce(ctx, k)
	}
	return h, err
}

func (c *client) callOnce(ctx context.Context, k request) (http.Header, error) {
	timeout := k.timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	body := k.body
	if k.json != nil {
		b, err := json.Marshal(k.json)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, k.method, c.id.base()+k.path, body)
	if err != nil {
		return nil, err
	}
	if k.body != nil && k.size >= 0 {
		req.ContentLength = k.size
	}
	req.Header.Set("Accept", "application/json, application/problem+json")
	switch {
	case k.json != nil:
		req.Header.Set("Content-Type", "application/json")
	case k.ctype != "":
		req.Header.Set("Content-Type", k.ctype)
	}
	if k.token != "" {
		req.Header.Set("Authorization", "Bearer "+k.token)
	}
	for n, v := range k.header {
		req.Header.Set(n, v)
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("cannot reach %s: %w", c.id.base(), unwrapURL(err))
	}
	defer resp.Body.Close()
	if v := resp.Header.Get(versionHeader); v != "" {
		c.version = v
		if c.onVersion != nil {
			if err := c.onVersion(ctx, v); err != nil {
				return nil, err
			}
		}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("cannot read the answer of %s: %w", c.id.base(), err)
	}
	if resp.StatusCode >= 300 {
		return resp.Header, c.answerError(resp, b)
	}
	if k.out == nil {
		return resp.Header, nil
	}
	if err := json.Unmarshal(b, k.out); err != nil {
		return resp.Header, c.genericError(resp)
	}
	return resp.Header, nil
}

// unwrapURL drops the "Post <url>:" of a *url.Error, so a message names the cause once.
func unwrapURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// answerError turns an answer of 300 or more into an error: the problem document, the
// {"error": "..."} of a format route, or the generic message of any other body.
func (c *client) answerError(resp *http.Response, b []byte) error {
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "application/problem+json" || ct == "application/json" {
		var p problem
		if json.Unmarshal(b, &p) == nil && (p.Title != "" || p.Detail != "") {
			if p.Status == 0 {
				p.Status = resp.StatusCode
			}
			return &problemError{p: p, server: c.id.base(), version: c.version}
		}
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return &problemError{p: problem{Type: "about:blank", Title: http.StatusText(resp.StatusCode), Detail: e.Error,
				Status: resp.StatusCode}, server: c.id.base(), version: c.version}
		}
	}
	return c.genericError(resp)
}

// genericError is the message of an answer that is not what the CLI asked for, such as the HTML
// page of a proxy.
func (c *client) genericError(resp *http.Response) error {
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = "no content type"
	}
	return &httpError{status: resp.StatusCode, msg: fmt.Sprintf("the server at %s answered %d with %s; a proxy in front of it may be down",
		c.id.base(), resp.StatusCode, ct)}
}

// httpError is an answer that is not a problem document.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// refused reports whether err is an answer of the server with a 4xx status: the server got the
// request and does not accept the credential or the input.
func refused(err error) bool {
	var pe *problemError
	var he *httpError
	switch {
	case errors.As(err, &pe):
		return pe.p.Status >= 400 && pe.p.Status < 500
	case errors.As(err, &he):
		return he.status >= 400 && he.status < 500
	}
	return false
}

// detailOf is the detail of a problem, else the text of err.
func detailOf(err error) string {
	var pe *problemError
	if errors.As(err, &pe) {
		if pe.p.Detail != "" {
			return pe.p.Detail
		}
		return pe.p.Title
	}
	return err.Error()
}

// enrollment is the answer of POST /api/devices/enroll.
type enrollment struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// enroll runs the device flow: it makes an Ed25519 key, asks the server for a code, prints the
// approval link (and opens it in the browser on a terminal), waits until a person approves the
// code, and returns the device file. It does not save it.
func (c *client) enroll(ctx context.Context, name string) (*deviceFile, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var e enrollment
	_, err = c.call(ctx, request{method: http.MethodPost, path: "/api/devices/enroll",
		json: map[string]string{"name": name, "public_key": base64.RawURLEncoding.EncodeToString(pub)}, out: &e})
	if err != nil {
		return nil, err
	}
	link := e.VerificationURL + "?code=" + e.UserCode
	fmt.Fprintf(os.Stderr, "Approve this computer for %s.\n", c.id.base())
	fmt.Fprintf(os.Stderr, "Open %s and approve code %s\n", link, e.UserCode)
	if isTerminal(os.Stdout) && openBrowser(link) {
		fmt.Fprintln(os.Stderr, "The CLI opened the link in your browser.")
	}
	fmt.Fprintf(os.Stderr, "Waiting for the approval. The code works for %d minutes.\n", max(1, e.ExpiresIn/60))

	expired := fmt.Errorf("the code %s expired before anyone approved it. Run the command again", e.UserCode)
	deadline := time.Now().Add(time.Duration(e.ExpiresIn) * time.Second)
	interval := time.Duration(max(1, e.Interval)) * time.Second
	for {
		if time.Now().After(deadline) {
			return nil, expired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var poll struct {
			DeviceID int64 `json:"device_id"`
		}
		_, err := c.call(ctx, request{method: http.MethodPost, path: "/api/devices/enroll/poll",
			json: map[string]string{"device_code": e.DeviceCode}, out: &poll})
		// The device flow code (RFC 8628) is the detail of the problem.
		code := ""
		if refused(err) {
			code = detailOf(err)
		}
		switch {
		case err == nil:
			return newDevice(c.id, poll.DeviceID, key)
		case code == "authorization_pending":
		case code == "slow_down":
			interval += 5 * time.Second
		case code == "expired_token":
			return nil, expired
		default:
			return nil, err
		}
	}
}

// challenge asks the server for a nonce of device d and returns it with its expiry. exchange asks
// for a token exchange nonce, which the registry's token endpoint and the exchange route take
// until publish.session_ttl ends; else the nonce is for one request and lives 60 s. keep is a
// token exchange nonce to keep: the server answers it again while it is valid. A request for a
// token exchange nonce carries the time and the device signature over it, so only the device gets
// a new nonce. A device that the server does not accept gets a 4xx answer (refused).
func (c *client) challenge(ctx context.Context, d *deviceFile, exchange bool, keep string) (string, time.Time, error) {
	var out struct {
		Nonce     string    `json:"nonce"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	in := map[string]any{"device_id": d.DeviceID, "exchange": exchange}
	if keep != "" {
		in["nonce"] = keep
	}
	if exchange {
		ts := time.Now().Unix()
		msg := challengePrefix + strconv.FormatInt(d.DeviceID, 10) + "\n" + strconv.FormatInt(ts, 10)
		in["ts"] = ts
		in["signature"] = d.sign(msg)
	}
	if _, err := c.call(ctx, request{method: http.MethodPost, path: "/api/publish/challenge", json: in, out: &out}); err != nil {
		return "", time.Time{}, err
	}
	if out.Nonce == "" || out.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("the server answered the challenge without a nonce and its expiry")
	}
	return out.Nonce, out.ExpiresAt, nil
}

// sign returns the Ed25519 signature of the device over msg, as base64url without padding.
func (d *deviceFile) sign(msg string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(d.key, []byte(msg)))
}

// exchangeSecret is the device proof of a token exchange: "trx.<device id>.<nonce>.<signature>",
// with the signature of the device over the nonce and the repo "*".
func exchangeSecret(d *deviceFile, nonce string) string {
	id := strconv.FormatInt(d.DeviceID, 10)
	return "trx." + id + "." + nonce + "." + d.sign(signPrefix+nonce+"\n"+id+"\n*")
}

// session is a publish session of one repo: the answer of the mint routes.
type session struct {
	ID        int64     `json:"id"`
	Token     string    `json:"token"`
	Repo      string    `json:"repo"`
	Format    string    `json:"format"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expires_at"`
}

// mintDevice mints a session for repo with the proof of device d: a one-request nonce and the
// device signature over the nonce and the repo.
func (c *client) mintDevice(ctx context.Context, d *deviceFile, repo string) (*session, error) {
	nonce, _, err := c.challenge(ctx, d, false, "")
	if err != nil {
		return nil, err
	}
	id := strconv.FormatInt(d.DeviceID, 10)
	var s session
	_, err = c.call(ctx, request{method: http.MethodPost, path: "/api/publish/sessions", json: map[string]any{
		"device_id": d.DeviceID, "repo": repo, "nonce": nonce, "signature": d.sign(signPrefix + nonce + "\n" + id + "\n" + repo),
	}, out: &s})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// mintCI mints a session for repo with the CI identity token tok. revision ("" for none) is the
// commit of the job, for the build record.
func (c *client) mintCI(ctx context.Context, tok, repo, revision string) (*session, error) {
	body := map[string]string{"token": tok, "repo": repo}
	if revision != "" {
		body["revision"] = revision
	}
	var s session
	_, err := c.call(ctx, request{method: http.MethodPost, path: "/api/publish/sessions/oidc",
		json: body, out: &s})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// closeSession ends session s. It waits at most 10 s, also when ctx has ended (Ctrl-C).
func (c *client) closeSession(s *session) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.call(ctx, request{method: http.MethodDelete, path: "/api/publish/sessions/" + strconv.FormatInt(s.ID, 10), token: s.Token})
	return err
}

// registryHosts asks the server for its Docker hosts (GET /docker/hosts): the registry host, then
// the upload host when the server has one (ORAS and Helm 4 push there).
func (c *client) registryHosts(ctx context.Context) ([]string, error) {
	var out struct {
		Registry string `json:"registry"`
		Upload   string `json:"upload"`
	}
	if _, err := c.call(ctx, request{method: http.MethodGet, path: "/docker/hosts", out: &out}); err != nil {
		return nil, err
	}
	hosts := []string{out.Registry}
	if out.Registry == "" {
		hosts = []string{c.id.hostKey()}
	}
	if out.Upload != "" && out.Upload != hosts[0] {
		hosts = append(hosts, out.Upload)
	}
	return hosts, nil
}
