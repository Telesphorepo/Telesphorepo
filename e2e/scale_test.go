//go:build scale

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The scale test seeds 100k and then 1M synthetic assets with "telesphorepo-server seed N"
// and measures request-path operations over HTTP at both sizes. An operation fails when its
// p95 at 1M is more than 2x its p95 at 100k. scaleFloor keeps sub-millisecond noise from
// failing the comparison: the 1M p95 must also exceed the 100k p95 by more than scaleFloor.
const (
	scaleWarmup  = 30
	scaleSamples = 400
	scaleFloor   = 2 * time.Millisecond
)

var scaleOps = []string{"maven-get", "asset-page", "search-rare", "search-common", "token-auth"}

func TestScale(t *testing.T) {
	results := map[int]map[string]time.Duration{}
	for _, n := range []int{100_000, 1_000_000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) { results[n] = scaleRun(t, n) })
		if t.Failed() {
			t.FailNow()
		}
	}
	small, large := results[100_000], results[1_000_000]
	t.Logf("%-14s %12s %12s %12s", "operation", "p95 100k", "p95 1M", "limit")
	for _, op := range scaleOps {
		a, b := small[op], large[op]
		limit := max(2*a, a+scaleFloor)
		t.Logf("%-14s %12s %12s %12s", op, a, b, limit)
		if b > limit {
			t.Errorf("%s: p95 %s at 1M is more than 2x the p95 %s at 100k", op, b, a)
		}
	}
}

// scaleRun prepares a server whose database holds n seeded assets and returns the p95 of
// each operation in scaleOps.
//
// A first server creates the database, the admin user and a token on a named data volume.
// After it stops, a one-shot container of the same image runs "seed n" on the volume, and a
// second server starts on the seeded volume for the measurements.
func scaleRun(t *testing.T, n int) map[string]time.Duration {
	vol := RandName("telesphorepo-scale")
	t.Cleanup(func() { removeVolume(t, vol) })

	first := StartServer(t, ServerOpts{DataVolume: vol})
	const user, pass = "scale", "scale-password-1"
	first.CreateUser(t, user, pass, AllServerPerms...)
	auth := Basic{user, first.CreateToken(t, user, pass, "scale")}
	terminate(t, first)

	runSeed(t, vol, n)

	s := StartServer(t, ServerOpts{DataVolume: vol})
	repos := scaleRepos(t, s, auth)
	waitSettled(t, s, auth, repos)

	var mvn string
	for _, r := range repos {
		if r.Format == "maven" {
			mvn = r.Name
			break
		}
	}
	if mvn == "" {
		t.Fatalf("seed %d created no maven repo: %+v", n, repos)
	}
	var page struct {
		Items []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+mvn+"/assets?limit=100", nil, auth, &page)
	if len(page.Items) == 0 {
		t.Fatalf("seeded maven repo %s has no assets", mvn)
	}
	sample := page.Items[len(page.Items)/2]
	base := []rune(path.Base(sample.Path))
	common := strings.TrimPrefix(path.Ext(sample.Path), ".")
	if len([]rune(common)) < 3 && len(base) >= 3 {
		common = string(base[:3])
	}
	t.Logf("seed %d: maven repo %s, sample %s, common term %q", n, mvn, sample.Path, common)

	get := func(p string, hdr map[string]string, ok ...int) func(t *testing.T) []byte {
		return func(t *testing.T) []byte {
			req := s.NewRequest(t, http.MethodGet, p, nil)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			resp, b := s.Do(t, req, auth)
			if !slices.Contains(ok, resp.StatusCode) {
				t.Fatalf("GET %s: status %d: %s", p, resp.StatusCode, b)
			}
			return b
		}
	}
	// The seeded rows have no blobs: a matching If-None-Match answers 304 from the row alone.
	mavenGet := get("/maven/"+mvn+"/"+sample.Path, map[string]string{"If-None-Match": `"` + sample.SHA256 + `"`},
		http.StatusNotModified, http.StatusOK)
	searchRare := get("/api/search?limit=50&q="+url.QueryEscape(sample.Path), nil, http.StatusOK)
	searchCommon := get("/api/search?limit=50&q="+url.QueryEscape(common), nil, http.StatusOK)
	tokenAuth := get("/api/me", nil, http.StatusOK)

	// Each asset-page sample fetches the page after the previous one.
	cursor := ""
	assetPage := func(t *testing.T) []byte {
		b := get("/api/repos/"+mvn+"/assets?limit=100&after="+url.QueryEscape(cursor), nil, http.StatusOK)(t)
		var next struct {
			Next *string `json:"next"`
		}
		if err := json.Unmarshal(b, &next); err != nil {
			t.Fatalf("decode asset page: %v", err)
		}
		cursor = ""
		if next.Next != nil {
			cursor = *next.Next
		}
		return b
	}

	if b := searchRare(t); !strings.Contains(string(b), sample.Path) {
		t.Fatalf("search for %q does not find it: %s", sample.Path, b)
	}

	out := map[string]time.Duration{
		"maven-get":     p95(t, mavenGet),
		"asset-page":    p95(t, assetPage),
		"search-rare":   p95(t, searchRare),
		"search-common": p95(t, searchCommon),
		"token-auth":    p95(t, tokenAuth),
	}
	for _, op := range scaleOps {
		t.Logf("seed %d: %-14s p95 %s", n, op, out[op])
	}
	return out
}

// p95 runs op scaleWarmup times, then scaleSamples times, and returns the 95th percentile of
// the timed runs. Each run is one HTTP round trip with the body read in full.
func p95(t *testing.T, op func(t *testing.T) []byte) time.Duration {
	t.Helper()
	for range scaleWarmup {
		op(t)
	}
	d := make([]time.Duration, scaleSamples)
	for i := range d {
		start := time.Now()
		op(t)
		d[i] = time.Since(start)
	}
	slices.Sort(d)
	return d[(len(d)*95+99)/100-1]
}

type scaleRepo struct {
	Name   string `json:"name"`
	Format string `json:"format"`
}

// scaleRepos lists every repo, following the next cursor when the list is paginated.
func scaleRepos(t *testing.T, s *Server, auth Auth) []scaleRepo {
	t.Helper()
	var all []scaleRepo
	after := ""
	for {
		st, b := s.API(t, http.MethodGet, "/api/repos?limit=100&after="+url.QueryEscape(after), nil, auth)
		if st != http.StatusOK {
			t.Fatalf("list repos: status %d: %s", st, b)
		}
		var rows []scaleRepo
		if err := json.Unmarshal(b, &rows); err == nil {
			return append(all, rows...)
		}
		var p struct {
			Items []scaleRepo `json:"items"`
			Next  *string     `json:"next"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("decode repos %s: %v", b, err)
		}
		all = append(all, p.Items...)
		if p.Next == nil || *p.Next == "" {
			return all
		}
		after = *p.Next
	}
}

// waitSettled waits until no repo reports pending background work in /api/repos/{name}/status,
// so that index rebuilds of the seeded data do not run during the measurements.
func waitSettled(t *testing.T, s *Server, auth Auth, repos []scaleRepo) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Minute)
	for _, r := range repos {
		for {
			var st struct {
				Pending int64 `json:"pending"`
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+r.Name+"/status", nil, auth, &st)
			if st.Pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("repo %s: still %d pending", r.Name, st.Pending)
			}
			time.Sleep(time.Second)
		}
	}
}

// runSeed runs "telesphorepo-server seed n" in a one-shot container of the server image on vol.
func runSeed(t *testing.T, vol string, n int) {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	c, err := testcontainers.Run(ctx, ServerImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithCmd("seed", strconv.Itoa(n)),
		testcontainers.WithMounts(testcontainers.VolumeMount(vol, "/data")),
		testcontainers.WithWaitStrategyAndDeadline(40*time.Minute, wait.ForExit()),
	)
	registerCleanup(t, c, "seed")
	if err != nil {
		out := ""
		if c != nil {
			out = containerLogs(c)
		}
		t.Fatalf("seed %d: %v\n%s", n, err, out)
	}
	st, err := c.State(ctx)
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if st.ExitCode != 0 {
		t.Fatalf("seed %d: exit %d:\n%s", n, st.ExitCode, containerLogs(c))
	}
	t.Logf("seed %d: %s", n, time.Since(start).Round(time.Second))
}

// terminate stops and removes the server container now; its data volume stays.
func terminate(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.Terminate(ctx); err != nil {
		t.Fatalf("stop server %s: %v", s.Alias, err)
	}
}

func removeVolume(t *testing.T, vol string) {
	if out, err := exec.Command(Engine.CLI, "volume", "rm", "-f", vol).CombinedOutput(); err != nil {
		t.Errorf("remove volume %s: %v\n%s", vol, err, out)
	}
}
