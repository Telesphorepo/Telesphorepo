//go:build scale

package e2e

// The scale zone (tag scale) needs no container engine. It builds the server for this computer,
// seeds a data folder with 100k and one with 1M synthetic assets ("telesphorepo-server seed N"),
// copies the 100k folder for an A/A control, and runs the three servers at once, pinned to their
// own CPUs. It sends each operation to the three servers in a random order, round after round,
// so that whatever the machine does meanwhile reaches all three alike, and compares the medians
// of the same request at 1M and at 100k block by block.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	scaleBlocks    = 20 // measured blocks of each operation
	scaleRounds    = 75 // rounds of a block: each round sends the operation once to each server
	scaleBootstrap = 10_000
	// The verdict limit of the ratio 1M/100k of an operation: twice the time at 100k, or the
	// time at 100k plus scaleFloor when that is more, so that a request of a fraction of a
	// millisecond does not fail on a few hundred microseconds.
	scaleRatioLimit = 2.0
	scaleFloor      = 2 * time.Millisecond
	// The A/A band: the ratio of two servers of the same data must stay within 0.8 to 1.25 (the
	// same factor either way). A machine that moves a ratio by more than a quarter could move a
	// real 1.6 to the limit, so its run says nothing about regressions.
	scaleAALow, scaleAAHigh = 0.8, 1.25
	// Warm-up: blocks of scaleWarmBlock requests per server until two consecutive block medians
	// of every server agree within scaleWarmAgree, at most scaleWarmMax blocks. A burst of
	// scaleBurst concurrent requests first: more than the reader connections of the server, so
	// that each of them serves the operation (each connection has its own page cache).
	scaleWarmBlock = 50
	scaleWarmMax   = 20
	scaleWarmAgree = 0.05
	scaleBurst     = 32
)

// The bootstrap admin of every data folder. Its read token authenticates every request.
const scaleAdmin, scalePass = "admin", "scale-admin-password-1"

// scaleDomain is the repo domain of the first seeded maven repo (the seed names its repos
// seed-<format>-<nn>), set by the environment of every measured server.
const scaleDomain, scaleDomainRepo = "maven.scale.example", "seed-maven-00"

// scaleServer is one server of the comparison.
type scaleServer struct {
	name   string // "100k", "1M" or "A/A" (the copy of the 100k data)
	srv    *nativeServer
	client *http.Client
	token  string
	cursor string // the asset-page walk
}

// scaleOp is one request path. path gives the path and query of the next request to s; next,
// when set, reads the answer of a measured request (the cursor of the next page).
type scaleOp struct {
	name string
	path func(s *scaleServer) string
	hdr  map[string]string
	ok   []int
	next func(s *scaleServer, body []byte) error
}

func TestScale(t *testing.T) {
	t.Parallel()
	scaleQuietGuard(t)
	serverCPUs := scalePin(t)
	bin := scaleServerBin(t)

	sizes := []int{100_000, 1_000_000}
	dirs := []string{t.TempDir(), t.TempDir()}
	tokens := make([]string, len(sizes))
	t.Run("seed", func(t *testing.T) {
		for i, n := range sizes {
			t.Run(strconv.Itoa(n), func(t *testing.T) {
				t.Parallel()
				tokens[i] = scalePrepare(t, bin, dirs[i], n)
			})
		}
	})
	if t.Failed() {
		t.FailNow()
	}
	aaDir := t.TempDir()
	if err := copyDir(dirs[0], aaDir); err != nil {
		t.Fatalf("copy the 100k data for the A/A control: %v", err)
	}

	env := []string{"TR_BOOTSTRAP_ADMIN_USER=" + scaleAdmin, "TR_BOOTSTRAP_ADMIN_PASSWORD=" + scalePass,
		"TR_REPO_DOMAINS=" + scaleDomain + "=" + scaleDomainRepo}
	if serverCPUs != nil {
		// The Go runtime of each server sizes itself to its own CPUs, not to the client's that it
		// starts on.
		env = append(env, "GOMAXPROCS="+strconv.Itoa(len(serverCPUs)))
	}
	start := func(cmd *exec.Cmd) error { return startPinned(cmd, serverCPUs) }
	var servers []*scaleServer
	for _, c := range []struct{ name, dir, token string }{{"100k", dirs[0], tokens[0]}, {"1M", dirs[1], tokens[1]}, {"A/A", aaDir, tokens[0]}} {
		s := &scaleServer{name: c.name, srv: startNativeServer(t, bin, c.dir, env, start), token: c.token,
			client: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 2 * scaleBurst}}}
		if serverCPUs != nil {
			got, err := affinityOf(s.srv.cmd.Process.Pid)
			if err != nil || !slices.Equal(got, serverCPUs) {
				t.Fatalf("server %s (pid %d) runs on CPUs %s (%v), want %s", c.name, s.srv.cmd.Process.Pid, formatCPUList(got), err, formatCPUList(serverCPUs))
			}
		}
		servers = append(servers, s)
	}
	for _, s := range servers {
		scaleSettled(t, s)
	}
	small := servers[0]

	// The sample is a path of the first Maven repo at 100k. seed numbers the rows of each repo
	// from 0 and makes each path from its number, so the 100k rows of a repo are rows of the
	// same repo at 1M: both sizes answer the same request.
	mvn := ""
	for _, r := range scaleRepos(t, small) {
		if r.Format == "maven" {
			mvn = r.Name
			break
		}
	}
	if mvn != scaleDomainRepo {
		t.Fatalf("the first maven repo is %q, want %s", mvn, scaleDomainRepo)
	}
	var page struct {
		Items []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"items"`
	}
	if err := json.Unmarshal(scaleGet(t, small, "/api/repos/"+mvn+"/assets?limit=100"), &page); err != nil || len(page.Items) == 0 {
		t.Fatalf("assets of %s at 100k: %v %+v", mvn, err, page)
	}
	sample := page.Items[len(page.Items)/2]
	common := strings.TrimPrefix(path.Ext(sample.Path), ".")
	if base := []rune(path.Base(sample.Path)); len([]rune(common)) < 3 && len(base) >= 3 {
		common = string(base[:3])
	}
	// shared is the artifact folder of the sample (com/example/libN/). Every seeded Maven repo
	// holds it with a version per 1,000 rows, so its rarest segment (libN) holds fewer than the
	// first probe of a search counts to at 100k, and more at 1M: the second probe decides there.
	shared := path.Dir(path.Dir(sample.Path)) + "/"

	ops := []scaleOp{
		{name: "maven-get", path: func(*scaleServer) string { return "/maven/" + mvn + "/" + sample.Path },
			// The seeded rows have no blobs: a matching If-None-Match answers 304 from the row alone.
			hdr: map[string]string{"If-None-Match": `"` + sample.SHA256 + `"`}, ok: []int{http.StatusNotModified}},
		// The same file through the repo domain: the host check, the rewrite, the same route.
		{name: "domain-get", path: func(*scaleServer) string { return "/" + sample.Path },
			hdr: map[string]string{"Host": scaleDomain, "If-None-Match": `"` + sample.SHA256 + `"`}, ok: []int{http.StatusNotModified}},
		{name: "asset-page", ok: []int{http.StatusOK},
			path: func(s *scaleServer) string {
				return "/api/repos/" + mvn + "/assets?limit=100&after=" + url.QueryEscape(s.cursor)
			},
			// Each request reads the page after the previous one, and the walk starts again
			// after the last page.
			next: func(s *scaleServer, body []byte) error {
				var p struct {
					Next *string `json:"next"`
				}
				if err := json.Unmarshal(body, &p); err != nil {
					return err
				}
				s.cursor = ""
				if p.Next != nil {
					s.cursor = *p.Next
				}
				return nil
			}},
		{name: "search-rare", path: func(*scaleServer) string { return "/api/search?limit=50&q=" + url.QueryEscape(sample.Path) }, ok: []int{http.StatusOK}},
		{name: "search-common", path: func(*scaleServer) string { return "/api/search?limit=50&q=" + url.QueryEscape(common) }, ok: []int{http.StatusOK}},
		{name: "search-shared", path: func(*scaleServer) string { return "/api/search?limit=50&q=" + url.QueryEscape(shared) }, ok: []int{http.StatusOK}},
		{name: "token-auth", path: func(*scaleServer) string { return "/api/me" }, ok: []int{http.StatusOK}},
		{name: "repo-page", path: func(*scaleServer) string { return "/api/repos?limit=50" }, ok: []int{http.StatusOK}},
		// The versions of an RPM package: the seek of rpm_packages_order (seed names its packages
		// seedpkg<n>, so the first ones are at every size).
		{name: "rpm-versions", path: func(*scaleServer) string { return "/api/repos/seed-rpm-00/versions?limit=20&path=Packages/seedpkg1/" }, ok: []int{http.StatusOK}},
	}
	for _, s := range servers {
		for _, op := range ops[:2] { // maven-get and domain-get: 304 for the sample
			if _, _, err := scaleDo(s, op, false); err != nil {
				t.Fatalf("%s of the sample %s at %s: %v", op.name, sample.Path, s.name, err)
			}
		}
		// The 304 of domain-get comes from the repo-domain rewrite only while the host check
		// runs: base_url (TR_BASE_URL of the native server) is set, so another host gets 421.
		other := scaleOp{path: func(*scaleServer) string { return "/" + sample.Path },
			hdr: map[string]string{"Host": "other.scale.example"}, ok: []int{http.StatusMisdirectedRequest}}
		if _, _, err := scaleDo(s, other, false); err != nil {
			t.Fatalf("a host that is no repo domain at %s: %v", s.name, err)
		}
		for _, q := range []string{ops[3].path(s), ops[5].path(s)} { // search-rare and search-shared
			if b := scaleGet(t, s, q); !strings.Contains(string(b), sample.Path) {
				t.Fatalf("%s at %s does not find %q: %s", q, s.name, sample.Path, b)
			}
		}
	}
	t.Logf("maven repo %s, sample %s at every size, common term %q, shared folder %q", mvn, sample.Path, common, shared)

	// Warm-up. Go compiles ahead of time, so there is no JIT to warm; what the first requests pay
	// for is cold state: the page cache of each SQLite reader connection, the OS file cache and
	// the mapped pages of the database, the client's keep-alive connections, the heap growing to
	// its steady size, and the CPU caches. A measurement starts once that state stops changing.
	seed := uint64(time.Now().UnixNano())
	rng := rand.New(rand.NewPCG(seed, seed>>1))
	unsettled := map[string]bool{}
	for _, op := range ops {
		for _, s := range servers {
			if err := scaleBurstOf(s, op); err != nil {
				t.Fatalf("warm-up burst of %s at %s: %v", op.name, s.name, err)
			}
		}
		prev := make([]float64, len(servers))
		blocks, settled := 0, false
		for blocks < scaleWarmMax && !settled {
			blocks++
			d := make([][]float64, len(servers))
			for range scaleWarmBlock {
				for _, si := range rng.Perm(len(servers)) {
					d[si] = append(d[si], scaleMeasure(t, servers[si], op))
				}
			}
			settled = blocks > 1
			for si := range servers {
				m := median(d[si])
				if math.Abs(m-prev[si]) > scaleWarmAgree*prev[si] {
					settled = false
				}
				prev[si] = m
			}
		}
		if !settled {
			unsettled[op.name] = true
		}
		t.Logf("warm-up %-14s %2d blocks of %d, settled: %v", op.name, blocks, scaleWarmBlock, settled)
	}

	// The measurement: each round sends every operation, in a random order, to the three
	// servers, in a random order.
	samples := make([][][][]float64, len(ops)) // op, server, block, request (ms)
	for oi := range ops {
		samples[oi] = make([][][]float64, len(servers))
		for si := range servers {
			samples[oi][si] = make([][]float64, scaleBlocks)
		}
	}
	began := time.Now()
	for b := range scaleBlocks {
		for range scaleRounds {
			for _, oi := range rng.Perm(len(ops)) {
				for _, si := range rng.Perm(len(servers)) {
					samples[oi][si][b] = append(samples[oi][si][b], scaleMeasure(t, servers[si], ops[oi]))
				}
			}
		}
	}
	t.Logf("measured %d blocks of %d rounds in %s (order seed %d)", scaleBlocks, scaleRounds, time.Since(began).Round(time.Second), seed)

	scaleReport(t, ops, samples, unsettled)
}

// scaleVerdict is the outcome of an operation or of the run.
type scaleVerdict string

const (
	verdictPass         scaleVerdict = "PASS"
	verdictFail         scaleVerdict = "FAIL"
	verdictInconclusive scaleVerdict = "INCONCLUSIVE"
	verdictNoisy        scaleVerdict = "NOISY"
)

// scaleReport prints the table and the verdicts, and fails the test on a regression. Per
// operation: the median and p95 of every request at each server; the median of the block
// ratios 1M/100k and A/A/100k (the ratio of the block medians) with a 95% bootstrap interval;
// the limit; the verdict. FAIL: the interval lies above the limit; PASS: below it;
// INCONCLUSIVE: it holds the limit. When an A/A interval leaves the A/A band the whole run is
// NOISY and judges nothing. E2E_SCALE_VERDICT names a file that gets the verdict of the run.
func scaleReport(t *testing.T, ops []scaleOp, samples [][][][]float64, unsettled map[string]bool) {
	t.Helper()
	boot := rand.New(rand.NewPCG(1, 2)) // the interval is a function of the samples alone
	var tbl strings.Builder
	fmt.Fprintf(&tbl, "%-14s %9s %9s %9s %9s %9s %9s | %-23s %6s | %-23s | %s\n", "operation",
		"med 100k", "p95 100k", "med 1M", "p95 1M", "med A/A", "p95 A/A", "1M/100k [95% CI]", "limit", "A/A/100k [95% CI]", "verdict")
	var fails, inconclusive, noisy []string
	for oi, op := range ops {
		var med, p95 [3]float64
		for si := range 3 {
			all := slices.Concat(samples[oi][si]...)
			slices.Sort(all)
			med[si], p95[si] = quantile(all, 0.5), quantile(all, 0.95)
		}
		ratioL, ratioA := make([]float64, scaleBlocks), make([]float64, scaleBlocks)
		for b := range scaleBlocks {
			s := median(samples[oi][0][b])
			ratioL[b] = median(samples[oi][1][b]) / s
			ratioA[b] = median(samples[oi][2][b]) / s
		}
		rL, loL, hiL := bootstrapMedian(ratioL, boot)
		rA, loA, hiA := bootstrapMedian(ratioA, boot)
		floorMS := float64(scaleFloor) / float64(time.Millisecond)
		limit := max(scaleRatioLimit, (med[0]+floorMS)/med[0])
		v := verdictInconclusive
		switch {
		case loL > limit:
			v = verdictFail
		case hiL < limit:
			v = verdictPass
		}
		note := ""
		if v == verdictFail && unsettled[op.name] {
			v, note = verdictInconclusive, " (warm-up did not settle)"
		}
		if loA < scaleAALow || hiA > scaleAAHigh {
			noisy = append(noisy, fmt.Sprintf("%s [%.2f, %.2f]", op.name, loA, hiA))
			note += " (A/A out of band)"
		}
		switch v {
		case verdictFail:
			fails = append(fails, fmt.Sprintf("%s: the 1M/100k ratio is %.2f [%.2f, %.2f], above the limit %.2f (median %.3f ms at 1M, %.3f ms at 100k)",
				op.name, rL, loL, hiL, limit, med[1], med[0]))
		case verdictInconclusive:
			inconclusive = append(inconclusive, fmt.Sprintf("%s [%.2f, %.2f] holds the limit %.2f", op.name, loL, hiL, limit))
		}
		fmt.Fprintf(&tbl, "%-14s %9.3f %9.3f %9.3f %9.3f %9.3f %9.3f | %5.2f [%5.2f, %5.2f]    %6.2f | %5.2f [%5.2f, %5.2f]     | %s%s\n",
			op.name, med[0], p95[0], med[1], p95[1], med[2], p95[2], rL, loL, hiL, limit, rA, loA, hiA, v, note)
	}
	t.Logf("times in ms; ratios are medians of %d block ratios (block medians of %d requests); A/A band [%.2f, %.2f]\n%s",
		scaleBlocks, scaleRounds, scaleAALow, scaleAAHigh, tbl.String())

	run, detail := verdictPass, "no operation slows down more than its limit at 1M"
	switch {
	case len(noisy) > 0:
		run, detail = verdictNoisy, "the A/A control moved beyond its band ("+strings.Join(noisy, ", ")+"): the machine was not quiet, so this run judges nothing. Rerun on a quiet machine"
	case len(fails) > 0:
		run, detail = verdictFail, strings.Join(fails, "; ")
		for _, f := range fails {
			t.Errorf("regression: %s", f)
		}
	case len(inconclusive) > 0:
		run, detail = verdictInconclusive, "the interval holds the limit for "+strings.Join(inconclusive, ", ")+": not a regression; rerun to decide"
	}
	t.Logf("\n==== scale verdict: %s ====\n%s", run, detail)
	if f := os.Getenv("E2E_SCALE_VERDICT"); f != "" {
		if err := os.WriteFile(f, []byte(string(run)+"\n"+detail+"\n"), 0o644); err != nil {
			t.Errorf("write %s: %v", f, err)
		}
	}
}

// bootstrapMedian returns the median of xs and the 2.5th and 97.5th percentiles of the medians
// of scaleBootstrap resamples of xs (with replacement).
func bootstrapMedian(xs []float64, rng *rand.Rand) (m, lo, hi float64) {
	meds := make([]float64, scaleBootstrap)
	re := make([]float64, len(xs))
	for i := range meds {
		for j := range re {
			re[j] = xs[rng.IntN(len(xs))]
		}
		meds[i] = median(re)
	}
	slices.Sort(meds)
	return median(xs), quantile(meds, 0.025), quantile(meds, 0.975)
}

// median returns the median of xs; it sorts a copy.
func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return quantile(s, 0.5)
}

// quantile returns the q-quantile of the sorted xs, interpolated between neighbours.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	i := int(pos)
	if i+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[i] + (pos-float64(i))*(sorted[i+1]-sorted[i])
}

// scaleMeasure sends op to s once and returns the time from the request to the last byte of the
// answer, in milliseconds.
func scaleMeasure(t *testing.T, s *scaleServer, op scaleOp) float64 {
	t.Helper()
	d, _, err := scaleDo(s, op, true)
	if err != nil {
		t.Fatalf("%s at %s: %v", op.name, s.name, err)
	}
	return float64(d) / 1e6
}

// scaleDo sends op to s and reads the answer in full. With next it also hands the answer to
// op.next. It returns the time in nanoseconds and the body.
func scaleDo(s *scaleServer, op scaleOp, next bool) (int64, []byte, error) {
	p := op.path(s)
	req, err := http.NewRequest(http.MethodGet, s.srv.base+p, nil)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(scaleAdmin, s.token)
	for k, v := range op.hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	start := scaleNow()
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	d := scaleNow() - start
	if err != nil {
		return 0, nil, err
	}
	if !slices.Contains(op.ok, resp.StatusCode) {
		return 0, nil, fmt.Errorf("GET %s: status %d: %.300s", p, resp.StatusCode, b)
	}
	if next && op.next != nil {
		if err := op.next(s, b); err != nil {
			return 0, nil, fmt.Errorf("GET %s: %w", p, err)
		}
	}
	return d, b, nil
}

// scaleBurstOf sends scaleBurst concurrent streams of four requests of op to s.
func scaleBurstOf(s *scaleServer, op scaleOp) error {
	var wg sync.WaitGroup
	errs := make([]error, scaleBurst)
	for i := range scaleBurst {
		wg.Go(func() {
			for range 4 {
				if _, _, err := scaleDo(s, op, false); err != nil {
					errs[i] = err
					return
				}
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// scaleGet reads p from s with the token and fails the test unless the status is 200.
func scaleGet(t *testing.T, s *scaleServer, p string) []byte {
	t.Helper()
	_, b, err := scaleDo(s, scaleOp{path: func(*scaleServer) string { return p }, ok: []int{http.StatusOK}}, false)
	if err != nil {
		t.Fatalf("%s: %v", s.name, err)
	}
	return b
}

type scaleRepo struct {
	Name   string `json:"name"`
	Format string `json:"format"`
}

// scaleRepos lists every repo of s, page by page of GET /api/repos.
func scaleRepos(t *testing.T, s *scaleServer) []scaleRepo {
	t.Helper()
	var all []scaleRepo
	after := ""
	for {
		var page struct {
			Items []scaleRepo `json:"items"`
			Next  *string     `json:"next"`
		}
		if err := json.Unmarshal(scaleGet(t, s, "/api/repos?limit=200&after="+url.QueryEscape(after)), &page); err != nil {
			t.Fatalf("repos of %s: %v", s.name, err)
		}
		all = append(all, page.Items...)
		if page.Next == nil {
			return all
		}
		after = *page.Next
	}
}

// scaleSettled waits until no repo of s reports pending background work in
// /api/repos/{name}/status, so that index rebuilds of the seeded data do not run during the
// measurements.
func scaleSettled(t *testing.T, s *scaleServer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Minute)
	for _, r := range scaleRepos(t, s) {
		for {
			var st struct {
				Pending int64 `json:"pending"`
			}
			if err := json.Unmarshal(scaleGet(t, s, "/api/repos/"+r.Name+"/status"), &st); err != nil {
				t.Fatalf("status of %s at %s: %v", r.Name, s.name, err)
			}
			if st.Pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("repo %s at %s: still %d pending", r.Name, s.name, st.Pending)
			}
			time.Sleep(time.Second)
		}
	}
}

// scalePrepare makes the data folder dir of n seeded assets and returns a read token of the
// admin. A first server creates the database, the admin and the token; after it stops, "seed n"
// adds the rows.
func scalePrepare(t *testing.T, bin, dir string, n int) string {
	t.Helper()
	s := startNativeServer(t, bin, dir, []string{"TR_BOOTSTRAP_ADMIN_USER=" + scaleAdmin, "TR_BOOTSTRAP_ADMIN_PASSWORD=" + scalePass}, nil)
	jar := s.signIn(t, &totpUser{name: scaleAdmin, pass: scalePass})
	var tok struct {
		Token string `json:"token"`
	}
	s.mustAPI(t, jar, "", http.MethodPost, "/api/tokens", map[string]any{"name": "scale"}, &tok)
	if tok.Token == "" {
		t.Fatal("POST /api/tokens: no token")
	}
	s.stop()

	began := time.Now()
	cmd := exec.Command(bin, "seed", strconv.Itoa(n))
	cmd.Env = append(os.Environ(), "TR_DATA_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed %d: %v\n%s", n, err, out)
	}
	t.Logf("seed %d: %s", n, time.Since(began).Round(time.Second))
	return tok.Token
}

// scaleServerBin returns the server binary of E2E_SERVER_BIN, or builds one for this computer.
func scaleServerBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("E2E_SERVER_BIN"); bin != "" {
		return bin
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	name := "telesphorepo-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, "./cmd/telesphorepo-server")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/telesphorepo-server: %v\n%s", err, b)
	}
	return out
}

// scalePin pins this process (the client) and returns the CPUs of the servers, or nil on an OS
// without a CPU affinity API.
func scalePin(t *testing.T) []int {
	t.Helper()
	top, err := readTopology()
	if errors.Is(err, errNoAffinity) {
		t.Logf("CPU pinning: %s has no CPU affinity API; the servers and the client run unpinned", runtime.GOOS)
		return nil
	}
	if err != nil {
		t.Fatalf("CPU topology: %v", err)
	}
	server, client, how, err := scaleCPUSets(top)
	if err != nil {
		t.Fatalf("CPU sets: %v", err)
	}
	if err := pinSelf(client); err != nil {
		t.Fatalf("pin the client: %v", err)
	}
	runtime.GOMAXPROCS(len(client))
	t.Logf("CPU pinning: servers on CPUs %s (%s), client on CPUs %s", formatCPUList(server), how, formatCPUList(client))
	return server
}

// scaleQuietGuard refuses to start while an e2e run (its labelled containers) or another
// Telesphorepo server runs on this computer: they would share the CPUs and the disk with the
// measurement.
func scaleQuietGuard(t *testing.T) {
	t.Helper()
	var busy []string
	for _, cli := range []string{"podman", "docker"} {
		p, err := exec.LookPath(cli)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "ps", "-q", "--filter", "label=telesphorepo.e2e").Output()
		if n := len(strings.Fields(string(out))); err == nil && n > 0 {
			busy = append(busy, fmt.Sprintf("%d containers of an e2e run (%s ps --filter label=telesphorepo.e2e)", n, cli))
		}
	}
	var pids []string
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq telesphorepo-server.exe", "/FO", "CSV", "/NH").Output()
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Split(strings.TrimSpace(l), ","); len(f) > 1 && strings.EqualFold(strings.Trim(f[0], `"`), "telesphorepo-server.exe") {
				pids = append(pids, strings.Trim(f[1], `"`))
			}
		}
	} else {
		out, _ := exec.Command("pgrep", "-f", `(^|/)telesphorepo-server( |$)`).Output()
		pids = strings.Fields(string(out))
	}
	if len(pids) > 0 {
		busy = append(busy, "telesphorepo-server processes "+strings.Join(pids, ", "))
	}
	if len(busy) > 0 {
		t.Fatalf("the scale test needs a quiet machine, and this one runs %s. Stop them, then run it again.", strings.Join(busy, " and "))
	}
}

// copyDir copies the files of the folder src into the folder dst, with their permissions.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(to, info.Mode().Perm())
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, info.Mode().Perm())
	})
}
