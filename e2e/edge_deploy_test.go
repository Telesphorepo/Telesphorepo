//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// edgeDeploy is the deploy part of GET /api/edge/status.
type edgeDeploy struct {
	TokenSet  bool    `json:"token_set"`
	Auto      bool    `json:"auto"`
	Running   bool    `json:"running"`
	Step      string  `json:"step"`
	Unfit     string  `json:"unfit"`
	Wanted    string  `json:"wanted"`
	Deployed  string  `json:"deployed"`
	LastAt    *string `json:"last_at"`
	LastOK    bool    `json:"last_ok"`
	LastError string  `json:"last_error"`
	LastCall  *struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Status int    `json:"status"`
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"last_call"`
}

func deployStatus(t *testing.T, s *Server) (edgeDeploy, []byte) {
	t.Helper()
	st, body := s.API(t, http.MethodGet, "/api/edge/status", nil, s.Admin())
	if st != http.StatusOK {
		t.Fatalf("GET /api/edge/status = %d: %s", st, body)
	}
	var out struct {
		Deploy edgeDeploy `json:"deploy"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("status: %v\n%s", err, body)
	}
	return out.Deploy, body
}

// edgeSetting returns the item of key in the settings of the group edge.
func edgeSetting(t *testing.T, s *Server, key string) (json.RawMessage, bool) {
	t.Helper()
	var all struct {
		Groups []struct {
			ID       string `json:"id"`
			Settings []struct {
				Key    string          `json:"key"`
				Secret bool            `json:"secret"`
				Value  json.RawMessage `json:"value"`
			} `json:"settings"`
		} `json:"groups"`
	}
	s.MustAPI(t, http.MethodGet, "/api/settings", nil, s.Admin(), &all)
	for _, g := range all.Groups {
		for _, it := range g.Settings {
			if it.Key == key {
				if g.ID != "edge" {
					t.Errorf("%s is in the group %s, want edge", key, g.ID)
				}
				return it.Value, it.Secret
			}
		}
	}
	t.Fatalf("no setting %s", key)
	return nil, false
}

// TestEdgeDeploySettings covers the deploy of the edge Worker from the server without Cloudflare:
// the token is a secret that the settings never show, a deploy without a token or with an S3
// endpoint that names no Cloudflare account is refused before any call, and the hash of the
// wanted state follows a region change and a repo domain change.
func TestEdgeDeploySettings(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	s := StartServer(t, ServerOpts{Storage: "s3", S3: tg})
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.home_hint": "weur"}, s.Admin(), nil)

	if v, secret := edgeSetting(t, s, "edge.cloudflare_token"); !secret || string(v) != `{"set":false}` {
		t.Errorf("edge.cloudflare_token secret %v, value %s; want a secret that is not set", secret, v)
	}
	if v, _ := edgeSetting(t, s, "edge.auto_deploy"); string(v) != "true" {
		t.Errorf("edge.auto_deploy = %s, want true", v)
	}

	d, _ := deployStatus(t, s)
	if d.TokenSet || d.Auto || d.Running || d.LastAt != nil || d.Deployed != "" {
		t.Errorf("deploy before a token = %+v, want no token, not auto, nothing run", d)
	}
	if !strings.Contains(d.Unfit, "edge.cloudflare_token") {
		t.Errorf("deploy unfit = %q, want it to name edge.cloudflare_token", d.Unfit)
	}
	if d.Wanted == "" {
		t.Fatalf("deploy wanted is empty: the settings run edge delivery")
	}
	if st, b := s.API(t, http.MethodPost, "/api/edge/deploy", nil, s.Admin()); st != http.StatusConflict ||
		!strings.Contains(string(b), "edge-deploy-refused") || !strings.Contains(string(b), "edge.cloudflare_token") {
		t.Errorf("deploy without a token = %d: %s, want 409 edge-deploy-refused naming the token", st, b)
	}

	// A region change and a repo domain change change the wanted state.
	first := d.Wanted
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.regions": []string{"enam=" + tg.Bucket + "-enam"}}, s.Admin(), nil)
	d, _ = deployStatus(t, s)
	if d.Wanted == "" || d.Wanted == first {
		t.Errorf("wanted after a region change = %q, want a new hash (was %q)", d.Wanted, first)
	}
	second := d.Wanted
	repo := RandName("updates")
	s.CreateRepo(t, repo, "raw", true, nil)
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"repo_domains": []string{repo + ".example.com=" + repo}}, s.Admin(), nil)
	d, _ = deployStatus(t, s)
	if d.Wanted == "" || d.Wanted == second || d.Wanted == first {
		t.Errorf("wanted after a repo domain change = %q, want a new hash (was %q, then %q)", d.Wanted, first, second)
	}
	// The same settings give the same hash.
	again, _ := deployStatus(t, s)
	if again.Wanted != d.Wanted {
		t.Errorf("wanted changed without a settings change: %q, then %q", d.Wanted, again.Wanted)
	}

	// With a token, an S3 endpoint that is not R2's names no account: refused before any call.
	const token = "invalid-token-for-e2e-settings"
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.cloudflare_token": token}, s.Admin(), nil)
	if v, _ := edgeSetting(t, s, "edge.cloudflare_token"); string(v) != `{"set":true}` {
		t.Errorf("edge.cloudflare_token value %s, want {\"set\":true}", v)
	}
	d, body := deployStatus(t, s)
	if !d.TokenSet || !d.Auto || !strings.Contains(d.Unfit, "names no Cloudflare account") {
		t.Errorf("deploy with a token on SeaweedFS = %+v, want token set, auto, and the endpoint refused", d)
	}
	st, b := s.API(t, http.MethodPost, "/api/edge/deploy", nil, s.Admin())
	if st != http.StatusConflict || !strings.Contains(string(b), "edge-deploy-refused") || !strings.Contains(string(b), "names no Cloudflare account") {
		t.Errorf("deploy on SeaweedFS = %d: %s, want 409 edge-deploy-refused naming the endpoint", st, b)
	}
	_, settings := s.API(t, http.MethodGet, "/api/settings", nil, s.Admin())
	for what, b := range map[string][]byte{"status": body, "settings": settings, "deploy answer": b} {
		if strings.Contains(string(b), token) {
			t.Errorf("the %s shows the token", what)
		}
	}
	// Nothing ran: the job deploys only settings that can deploy.
	if d.LastAt != nil || d.Running {
		t.Errorf("deploy ran on settings that cannot deploy: %+v", d)
	}

	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.auto_deploy": false}, s.Admin(), nil)
	if d, _ := deployStatus(t, s); d.Auto {
		t.Errorf("deploy auto after edge.auto_deploy off = true")
	}
}

// TestEdgeDeployRefusedToken deploys with a token that Cloudflare refuses, on a server whose S3
// endpoint has the host of an R2 endpoint (a SeaweedFS gateway under that network alias), so the
// deploy has an account and calls the real Cloudflare API: the verify step fails, and the status
// and the audit log show the step, the call and Cloudflare's code and message, never the token.
// It needs the internet.
func TestEdgeDeployRefusedToken(t *testing.T) {
	t.Parallel()
	account := randHex(16)
	tg := StartOwnSeaweedFSAt(t, account+".r2.cloudflarestorage.com")
	// A Worker route names no port, so the deploy needs a base URL without one.
	alias := RandName("repo")
	s := StartServer(t, ServerOpts{Storage: "s3", S3: tg.S3Target, Alias: alias, Env: map[string]string{"TR_BASE_URL": "http://" + alias}})
	const token = "invalid-token-for-e2e"
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{
		"edge.home_hint": "weur", "edge.cloudflare_token": token, "edge.auto_deploy": false,
	}, s.Admin(), nil)

	d, _ := deployStatus(t, s)
	if d.Unfit != "" || !d.TokenSet || d.Auto {
		t.Fatalf("deploy = %+v, want one that can start, with a token, not auto", d)
	}
	st, b := s.API(t, http.MethodPost, "/api/edge/deploy", nil, s.Admin())
	if st != http.StatusAccepted {
		t.Fatalf("POST /api/edge/deploy = %d: %s, want 202", st, b)
	}

	var body []byte
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(300 * time.Millisecond) {
		d, body = deployStatus(t, s)
		if !d.Running && d.LastAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deploy did not end: %+v", d)
		}
	}
	if d.LastOK || d.Step != "verify" || !strings.Contains(d.LastError, "Verify the token") {
		t.Errorf("deploy = ok %v, step %q, error %q; want a failure of the step verify", d.LastOK, d.Step, d.LastError)
	}
	if c := d.LastCall; c == nil {
		t.Errorf("deploy last_call = null, want the Cloudflare call: %s", body)
	} else {
		if c.Method != http.MethodGet || c.Path != "/accounts/"+account+"/tokens/verify" || c.Status < 400 || c.Status > 499 {
			t.Errorf("deploy last_call = %s %s HTTP %d, want GET /accounts/%s/tokens/verify with a 4xx", c.Method, c.Path, c.Status, account)
		}
		if len(c.Errors) == 0 || c.Errors[0].Code == 0 || c.Errors[0].Message == "" {
			t.Errorf("deploy last_call errors = %+v, want Cloudflare's code and message", c.Errors)
		}
		for _, e := range c.Errors {
			if e.Code != 0 && !strings.Contains(d.LastError, e.Message) {
				t.Errorf("deploy last_error %q does not hold Cloudflare's message %q", d.LastError, e.Message)
			}
		}
	}
	if d.Deployed != "" {
		t.Errorf("deploy deployed = %q after a failed verify, want none", d.Deployed)
	}

	// The audit log records the deploy of the person, with its step.
	e := findAudit(auditAll(t, s, "action=edge.deploy"), "edge.deploy", s.AdminUser)
	if e == nil {
		t.Fatalf("no edge.deploy event of %s", s.AdminUser)
	}
	if e.Detail["ok"] != false || e.Detail["step"] != "verify" || e.Detail["trigger"] != "manual" {
		t.Errorf("edge.deploy detail = %v, want ok false, step verify, trigger manual", e.Detail)
	}

	raw, _ := json.Marshal(e)
	for what, b := range map[string][]byte{"status": body, "audit event": raw, "server log": []byte(s.Logs(t))} {
		if strings.Contains(string(b), token) {
			t.Errorf("the %s shows the token", what)
		}
	}
}
