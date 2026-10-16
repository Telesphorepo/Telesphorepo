package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrRefused is a remote that refused the username and password.
	ErrRefused = errors.New("the remote refused the username and password")
	// ErrNeedsCredentials is a remote that asks for a username and password that the proxy does
	// not have.
	ErrNeedsCredentials = errors.New("the remote needs a username and password")
	// ErrDenied is a remote that gave a bearer token but refused the request with it (401 or 403).
	// A registry answers so for a name that does not exist or that the credentials cannot read.
	ErrDenied = errors.New("the remote denied the request")
)

const (
	// defaultTokenLife is the life of a bearer token whose answer has no expires_in.
	defaultTokenLife = 60 * time.Second
	// maxTokenAnswer bounds the answer of a token endpoint.
	maxTokenAnswer = 1 << 20
)

// Challenge is one parsed WWW-Authenticate challenge.
type Challenge struct {
	Scheme string            // "bearer" or "basic", lower case
	Params map[string]string // keys in lower case
}

// ParseChallenges reads the WWW-Authenticate headers of h, e.g.
// `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`.
func ParseChallenges(h http.Header) []Challenge {
	var out []Challenge
	for _, v := range h.Values("WWW-Authenticate") {
		v = strings.TrimSpace(v)
		scheme, rest, _ := strings.Cut(v, " ")
		c := Challenge{Scheme: strings.ToLower(scheme), Params: map[string]string{}}
		for rest = strings.TrimSpace(rest); rest != ""; {
			k, after, ok := strings.Cut(rest, "=")
			if !ok {
				break
			}
			k = strings.ToLower(strings.TrimSpace(k))
			var val string
			after = strings.TrimSpace(after)
			if strings.HasPrefix(after, `"`) {
				end := 1
				var b strings.Builder
				for end < len(after) && after[end] != '"' {
					if after[end] == '\\' && end+1 < len(after) {
						end++
					}
					b.WriteByte(after[end])
					end++
				}
				val, after = b.String(), after[min(end+1, len(after)):]
			} else {
				val, after, _ = strings.Cut(after, ",")
				after = "," + after
			}
			c.Params[k] = strings.TrimSpace(val)
			rest = strings.TrimLeft(strings.TrimSpace(after), ",")
			rest = strings.TrimSpace(rest)
		}
		out = append(out, c)
	}
	return out
}

// Answer answers a 401 challenge with the headers of a retry and how long they stay good (0: as
// long as the credentials). A Bearer challenge requests realm?service=&scope= with GET and the
// credentials as HTTP Basic (when user is set), as the registry token protocol says, and returns
// "Authorization: Bearer <token>". A Basic challenge returns the Basic header of the
// credentials. cl sends the token request; nil is the proxy's client. The credentials go only to
// the realm, never into an error or a log.
func Answer(ctx context.Context, cl *http.Client, challenge http.Header, user, pass string) (http.Header, time.Duration, error) {
	if cl == nil {
		cl = client
	}
	cs := ParseChallenges(challenge)
	for _, c := range cs {
		if c.Scheme == "bearer" {
			return bearer(ctx, cl, c, user, pass)
		}
	}
	for _, c := range cs {
		if c.Scheme == "basic" {
			if user == "" {
				return nil, 0, ErrNeedsCredentials
			}
			r := &http.Request{Header: http.Header{}}
			r.SetBasicAuth(user, pass)
			return http.Header{"Authorization": {r.Header.Get("Authorization")}}, 0, nil
		}
	}
	return nil, 0, errors.New("the remote answered 401 without a challenge that the proxy can answer")
}

// bearer requests a token at the realm of c.
func bearer(ctx context.Context, cl *http.Client, c Challenge, user, pass string) (http.Header, time.Duration, error) {
	realm, err := url.Parse(c.Params["realm"])
	if err != nil || (realm.Scheme != "http" && realm.Scheme != "https") || realm.Host == "" {
		return nil, 0, errors.New("the remote's token realm is not an http or https address")
	}
	q := realm.Query()
	if s := c.Params["service"]; s != "" {
		q.Set("service", s)
	}
	for _, s := range strings.Fields(c.Params["scope"]) {
		q.Add("scope", s)
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "Telesphorepo proxy")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("the remote's token realm did not answer: %v", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if user == "" {
			return nil, 0, ErrNeedsCredentials
		}
		return nil, 0, ErrRefused
	case resp.StatusCode != http.StatusOK:
		return nil, 0, fmt.Errorf("the remote's token realm answered %s", resp.Status)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenAnswer)).Decode(&tok); err != nil {
		return nil, 0, errors.New("the remote's token realm sent no token")
	}
	t := tok.Token
	if t == "" {
		t = tok.AccessToken
	}
	if t == "" {
		return nil, 0, errors.New("the remote's token realm sent no token")
	}
	life := defaultTokenLife
	if tok.ExpiresIn > 0 {
		life = time.Duration(tok.ExpiresIn) * time.Second
	}
	return http.Header{"Authorization": {"Bearer " + t}}, life, nil
}
