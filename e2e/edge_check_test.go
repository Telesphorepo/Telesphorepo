//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// edgeCheckPath is the reserved path of the check of the edge Worker.
const edgeCheckPath = "/.well-known/telesphorepo-edge/check"

// TestEdgeCheck runs Check now (POST /api/edge/check) on a server on S3 with no Worker in front of
// it: the request of the check reaches the server itself, and the check says so. The reserved path
// refuses a request without the nonce of a running check, and the Worker configuration routes the
// path to the Worker. No Cloudflare account is needed.
func TestEdgeCheck(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	s := StartServer(t, ServerOpts{Storage: "s3", S3: tg})
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.home_hint": "weur"}, s.Admin(), nil)
	host := s.Alias + ":8080"

	type check struct {
		Outcome       string `json:"outcome"`
		OK            bool   `json:"ok"`
		Message       string `json:"message"`
		WorkerVersion string `json:"worker_version"`
	}
	var res check
	s.MustAPI(t, http.MethodPost, "/api/edge/check", nil, s.Admin(), &res)
	if res.Outcome != "server" || res.OK || !strings.Contains(res.Message, "reached the server, not the Worker") || !strings.Contains(res.Message, host) {
		t.Errorf("check = %+v, want outcome server with a message that names %s", res, host)
	}

	// One check at a time, at most one every 10 seconds.
	if st, b := s.API(t, http.MethodPost, "/api/edge/check", nil, s.Admin()); st != http.StatusTooManyRequests || !strings.Contains(string(b), "edge-check-busy") {
		t.Errorf("second check at once = %d: %s, want 429 edge-check-busy", st, b)
	}

	// The status keeps what the last check found.
	var st struct {
		CheckUnfit string `json:"check_unfit"`
		LastCheck  *check `json:"last_check"`
	}
	s.MustAPI(t, http.MethodGet, "/api/edge/status", nil, s.Admin(), &st)
	if st.CheckUnfit != "" || st.LastCheck == nil || st.LastCheck.Outcome != "server" {
		t.Errorf("status check_unfit %q, last_check %+v; want none and outcome server", st.CheckUnfit, st.LastCheck)
	}

	// The reserved path answers only the nonce of a running check; the server marks its own answer.
	for _, q := range []string{"", "?n=", "?n=" + strings.Repeat("A", 43)} {
		resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, edgeCheckPath+q, nil), nil)
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Telesphorepo-Edge-Check") != "server" {
			t.Errorf("GET %s%s = %d (Telesphorepo-Edge-Check %q): %s, want 403 from the server", edgeCheckPath, q, resp.StatusCode,
				resp.Header.Get("Telesphorepo-Edge-Check"), body)
		}
	}

	// The Worker configuration puts the reserved path on a route of the Worker.
	resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, "/api/edge/wrangler.jsonc", nil), s.Admin())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/edge/wrangler.jsonc = %d: %s", resp.StatusCode, body)
	}
	var cfg struct {
		Routes []string `json:"routes"`
	}
	if err := json.Unmarshal(parseWrangler(t, body), &cfg); err != nil {
		t.Fatalf("wrangler.jsonc: %v", err)
	}
	if want := host + "/.well-known/telesphorepo-edge/*"; !slices.Contains(cfg.Routes, want) {
		t.Errorf("routes = %v, want %s", cfg.Routes, want)
	}

	// A server on the local disk has no home bucket for the check request.
	local := StartServer(t, ServerOpts{})
	if st, b := local.API(t, http.MethodPost, "/api/edge/check", nil, local.Admin()); st != http.StatusConflict || !strings.Contains(string(b), "edge-not-configured") {
		t.Errorf("check on fs = %d: %s, want 409 edge-not-configured", st, b)
	}
}
