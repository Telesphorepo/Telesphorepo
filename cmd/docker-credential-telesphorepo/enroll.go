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
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// signPrefix starts the text that a device signs (the server's publish.SignedMessage).
const signPrefix = "telesphorepo publish v1\n"

// challengePrefix starts the text that a device signs to ask for a token exchange nonce (the
// server's publish.ChallengeMessage).
const challengePrefix = "telesphorepo challenge v1\n"

// apiError is an answer of the server with a status of 400 or more and its {"error": "..."}.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("the server answered %d: %s", e.status, e.msg) }

// client sends requests to one server, at its canonical URL only. It follows no redirect, so a
// credential never goes to another address.
type client struct {
	id   identity
	http *http.Client
}

func newClient(id identity) *client {
	return &client{id: id, http: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// do sends method path (under the base URL) with the JSON body in (nil: none) and decodes the
// JSON answer into out (nil: none). An answer of 300 or more is an *apiError.
func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.id.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "docker-credential-telesphorepo/"+version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", c.id.base(), err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("cannot read the answer of %s: %w", c.id.base(), err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &apiError{status: resp.StatusCode, msg: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s did not answer with JSON: %w", method, path, err)
	}
	return nil
}

// enrollment is the answer of POST /api/devices/enroll.
type enrollment struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// enroll makes an Ed25519 key, asks the server for a code, prints the approval link (and opens it
// in the browser when stdout is a terminal), waits until a person approves the code, and returns
// the device file. It does not save it.
func (c *client) enroll(ctx context.Context, name string) (*deviceFile, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var e enrollment
	err = c.do(ctx, http.MethodPost, "/api/devices/enroll", map[string]string{
		"name": name, "client": clientID, "public_key": base64.RawURLEncoding.EncodeToString(pub),
	}, &e)
	if err != nil {
		return nil, err
	}
	link := e.VerificationURL + "?code=" + e.UserCode
	fmt.Printf("Approve this computer to push to %s.\n", c.id.base())
	fmt.Printf("Open %s and approve code %s\n", link, e.UserCode)
	if isTerminal(os.Stdout) && openBrowser(link) {
		fmt.Println("The helper opened the link in your browser.")
	}
	fmt.Printf("Waiting for the approval. The code works for %d minutes.\n", max(1, e.ExpiresIn/60))

	expired := fmt.Errorf("the code %s expired before anyone approved it. Run the login again", e.UserCode)
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
		err := c.do(ctx, http.MethodPost, "/api/devices/enroll/poll", map[string]string{"device_code": e.DeviceCode}, &poll)
		var ae *apiError
		switch {
		case err == nil:
			return newDevice(c.id, poll.DeviceID, key)
		case errors.As(err, &ae) && ae.msg == "authorization_pending":
		case errors.As(err, &ae) && ae.msg == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &ae) && ae.msg == "expired_token":
			return nil, expired
		default:
			return nil, err
		}
	}
}

// challenge asks the server for a nonce of device d and returns it with its expiry. exchange asks
// for a token exchange nonce, which the registry's token endpoint takes until publish.session_ttl
// ends; else the nonce is for one session and lives 60 s. keep is a token exchange nonce to keep:
// the server answers it again while it is valid. A request for a token exchange nonce carries the
// time and the device signature over it, so only the device gets a new nonce. A device that the
// server does not accept gets a 4xx *apiError.
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
		in["signature"] = base64.RawURLEncoding.EncodeToString(ed25519.Sign(d.key, []byte(msg)))
	}
	if err := c.do(ctx, http.MethodPost, "/api/publish/challenge", in, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Nonce == "" || out.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("the server answered the challenge without a nonce and its expiry")
	}
	return out.Nonce, out.ExpiresAt, nil
}

// exchangeSecret is the password of a token exchange: "trx.<device id>.<nonce>.<signature>",
// with the Ed25519 signature of the device over the nonce and the repo "*", as base64url without
// padding.
func exchangeSecret(d *deviceFile, nonce string) string {
	msg := signPrefix + nonce + "\n" + strconv.FormatInt(d.DeviceID, 10) + "\n*"
	sig := ed25519.Sign(d.key, []byte(msg))
	return "trx." + strconv.FormatInt(d.DeviceID, 10) + "." + nonce + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// registryHosts asks the server for its Docker hosts (GET /docker/hosts): the registry host, then
// the upload host when the server has one (ORAS and Helm 4 push there).
func (c *client) registryHosts(ctx context.Context) ([]string, error) {
	var out struct {
		Registry string `json:"registry"`
		Upload   string `json:"upload"`
	}
	if err := c.do(ctx, http.MethodGet, "/docker/hosts", nil, &out); err != nil {
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

// isTerminal reports whether f is a terminal (a character device).
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// openBrowser opens url in the default browser. It returns false when this computer has no
// browser to open.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", url)
	case os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "":
		cmd = exec.Command("xdg-open", url)
	default:
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait()
	return true
}
