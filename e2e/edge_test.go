//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestEdgeWorkerDownload downloads the edge Worker folder (GET /api/edge/worker.zip) of a server
// on S3 with a home region and one region: the zip holds the bundle, package.json, the provision
// script and the server's wrangler.jsonc for the bundle, with the routes (a repo domain whole)
// and buckets of the settings. No Cloudflare account is needed.
func TestEdgeWorkerDownload(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	s := StartServer(t, ServerOpts{Storage: "s3", S3: tg})
	region := tg.Bucket + "-enam"
	updates := RandName("updates")
	s.CreateRepo(t, updates, "raw", true, nil)
	domain := updates + ".example.com"
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{
		"edge.home_hint": "weur", "edge.regions": []string{"enam=" + region}, "repo_domains": []string{domain + "=" + updates},
	}, s.Admin(), nil)

	var st struct {
		ServerVersion string `json:"server_version"`
		WorkerMissing string `json:"worker_missing"`
	}
	s.MustAPI(t, http.MethodGet, "/api/edge/status", nil, s.Admin(), &st)
	if st.WorkerMissing != "" {
		t.Fatalf("worker_missing = %q, want none", st.WorkerMissing)
	}

	resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, "/api/edge/worker.zip", nil), s.Admin())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/edge/worker.zip = %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="telesphorepo-edge_`) || !strings.HasSuffix(cd, `.zip"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open the zip: %v", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		files[f.Name] = b
	}
	for _, name := range []string{"dist/index.js", "package.json", "bun.lock", "README.md", "scripts/provision.ts", "wrangler.jsonc"} {
		if len(files["telesphorepo-edge/"+name]) == 0 {
			t.Errorf("the zip has no telesphorepo-edge/%s; it has %v", name, zipNames(files))
		}
	}
	// The provision script and the README are the files of edge/ in this tree.
	for _, name := range []string{"scripts/provision.ts", "README.md"} {
		want, err := os.ReadFile(filepath.Join(repoRoot(t), "edge", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if got := files["telesphorepo-edge/"+name]; !bytes.Equal(got, want) {
			t.Errorf("telesphorepo-edge/%s of the zip (%d bytes) differs from edge/%s (%d bytes)", name, len(got), name, len(want))
		}
	}

	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(files["telesphorepo-edge/package.json"], &pkg); err != nil {
		t.Fatalf("package.json: %v", err)
	}
	if pkg.Scripts["provision"] == "" || pkg.Scripts["deploy"] == "" || pkg.Scripts["build"] != "" {
		t.Errorf("package.json scripts = %v, want provision and deploy without build", pkg.Scripts)
	}
	if v := pkg.DevDependencies["wrangler"]; v == "" || strings.ContainsAny(v, "^~*") {
		t.Errorf("package.json wrangler = %q, want an exact version", v)
	}

	type config struct {
		Main      string   `json:"main"`
		NoBundle  bool     `json:"no_bundle"`
		Routes    []string `json:"routes"`
		Define    map[string]string
		R2Buckets []struct {
			Binding    string `json:"binding"`
			BucketName string `json:"bucket_name"`
		} `json:"r2_buckets"`
	}
	bundled := parseWrangler(t, files["telesphorepo-edge/wrangler.jsonc"])
	var cfg config
	if err := json.Unmarshal(bundled, &cfg); err != nil {
		t.Fatalf("wrangler.jsonc of the zip: %v\n%s", err, bundled)
	}
	if cfg.Main != "dist/index.js" || !cfg.NoBundle || cfg.Define != nil {
		t.Errorf("wrangler.jsonc main %q, no_bundle %v, define %v; want dist/index.js, true and no define", cfg.Main, cfg.NoBundle, cfg.Define)
	}
	base, err := url.Parse(s.InternalURL)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Routes, base.Host+"/maven/*") || !slices.Contains(cfg.Routes, base.Host+"/v2/*") {
		t.Errorf("routes = %v, want the maven and docker routes of %s", cfg.Routes, base.Host)
	}
	if !slices.Contains(cfg.Routes, domain+"/*") {
		t.Errorf("routes = %v, want the whole repo domain %s", cfg.Routes, domain)
	}
	buckets := map[string]string{}
	for _, b := range cfg.R2Buckets {
		buckets[b.Binding] = b.BucketName
	}
	if buckets["HOME"] != tg.Bucket || buckets["R2_ENAM"] != region {
		t.Errorf("r2_buckets = %v, want HOME %s and R2_ENAM %s", buckets, tg.Bucket, region)
	}

	// The wrangler.jsonc of a checkout has the same routes, with the sources and the define.
	resp, body = s.Do(t, s.NewRequest(t, http.MethodGet, "/api/edge/wrangler.jsonc", nil), s.Admin())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/edge/wrangler.jsonc = %d: %s", resp.StatusCode, body)
	}
	var checkout config
	if err := json.Unmarshal(parseWrangler(t, body), &checkout); err != nil {
		t.Fatalf("wrangler.jsonc: %v", err)
	}
	if checkout.Main != "src/index.ts" || checkout.NoBundle || checkout.Define["TELESPHOREPO_VERSION"] != `"`+st.ServerVersion+`"` {
		t.Errorf("checkout wrangler.jsonc main %q, no_bundle %v, define %v", checkout.Main, checkout.NoBundle, checkout.Define)
	}
	if !slices.Equal(checkout.Routes, cfg.Routes) {
		t.Errorf("checkout routes %v, zip routes %v", checkout.Routes, cfg.Routes)
	}

	// A server on the local disk cannot run edge delivery, so it has no Worker to hand out.
	local := StartServer(t, ServerOpts{})
	if st, b := local.API(t, http.MethodGet, "/api/edge/worker.zip", nil, local.Admin()); st != http.StatusConflict || !strings.Contains(string(b), "edge-not-configured") {
		t.Errorf("worker.zip on fs = %d: %s, want 409 edge-not-configured", st, b)
	}
}

// parseWrangler returns the JSON of a wrangler.jsonc that the server writes: the comment lines at
// its top, then the JSON.
func parseWrangler(t *testing.T, b []byte) []byte {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines = append(lines, l)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func zipNames(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
