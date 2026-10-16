package raw

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

var (
	rawProducts = []string{"atlas", "beacon", "cinder", "drift", "ember", "fathom", "glacier", "harbor"}
	rawTopics   = []string{
		"installation", "configuration", "authentication", "deployment", "upgrading", "backups",
		"monitoring", "troubleshooting", "performance", "security", "networking", "storage",
		"logging", "plugins", "migration", "high-availability", "command-line", "api-tokens",
	}
	rawPackages = []string{"core", "client", "server", "config", "storage", "net", "auth", "events", "query", "cli"}
	rawClasses  = []string{
		"Builder", "Client", "Config", "Context", "Decoder", "Encoder", "Error", "Event", "Handler",
		"Index", "Iterator", "Listener", "Manager", "Options", "Pool", "Reader", "Registry",
		"Request", "Response", "Router", "Schema", "Session", "Store", "Stream", "Token", "Writer",
	}
	rawPlatforms   = []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64"}
	rawBranchWords = []string{
		"fast", "index", "retry", "cache", "auth", "upload", "search", "metrics", "cleanup", "parser",
		"tls", "proxy", "export", "import", "batch", "queue",
	}
	rawModules = []string{"core", "api", "cli", "storage", "web", "worker", "integration"}
	rawActors  = []string{"mei-lin", "tomas-n", "priya-r", "renovate[bot]"}
)

// demoRawFiles is the size of the raw demo repo.
const demoRawFiles = 20_000

// Demo fills the raw demo repo (module.DemoData).
func (f *Format) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	files, err := planRaw(run, repo, rng)
	if err != nil {
		return err
	}
	run.Expect(len(files))
	for _, file := range files {
		if err := run.Add(repo, file); err != nil {
			return err
		}
	}
	return nil
}

// planRaw returns the files of the raw repo: documentation sites per product and version,
// release archives by year and month, and build logs per project, branch and build, about
// demoRawFiles in all.
func planRaw(run *demo.Run, repo *repos.Repo, rng *rand.Rand) ([]demo.File, error) {
	var out []demo.File
	var build int64 // the CI build record of the files being added; 0 = none
	add := func(p string, data []byte, by, at int64) {
		out = append(out, demo.File{Path: p, Data: data, Kind: assets.KindArtifact, By: by, At: at, Build: build})
	}

	// Documentation sites: docs/<product>/<version>/...
	for pi, prod := range rawProducts[:6] {
		versions := 4 + rng.IntN(4)
		minor := rng.IntN(3)
		offsets := demo.Times(rng, versions)
		for vi := range versions {
			v := fmt.Sprintf("%d.%d", 1+(minor+vi)/6, (minor+vi)%6)
			at := run.Anchor - offsets[vi]
			by := run.Uploader(rng, repo, pi)
			base := "docs/" + prod + "/" + v + "/"
			title := strings.ToUpper(prod[:1]) + prod[1:] + " " + v
			add(base+"index.html", htmlPage(title, "Documentation for "+title+"."), by, at)
			add(base+"404.html", htmlPage("Page not found", "This page does not exist in "+title+"."), by, at)
			add(base+"getting-started/index.html", htmlPage(title+": getting started", "Install "+prod+" and run your first job."), by, at)
			for _, t := range rawTopics[:8+rng.IntN(len(rawTopics)-8)] {
				add(base+"guides/"+t+"/index.html", htmlPage(title+": "+strings.ReplaceAll(t, "-", " "), "How to handle "+strings.ReplaceAll(t, "-", " ")+" in "+prod+"."), by, at)
			}
			for _, pkg := range rawPackages[:4+rng.IntN(len(rawPackages)-4)] {
				for _, c := range rawClasses[:6+rng.IntN(len(rawClasses)-6)] {
					add(base+"reference/"+pkg+"/"+c+".html", htmlPage(pkg+"."+c, "The "+c+" type of the "+pkg+" package."), by, at)
				}
			}
			add(base+"assets/css/site.css", []byte("body{font-family:system-ui,sans-serif;margin:0;color:#1d1d1f}\nmain{max-width:48rem;margin:2rem auto;padding:0 1rem}\na{color:#0b63c4}\ncode{font-size:.9em}\n"), by, at)
			add(base+"assets/js/search.js", []byte("// Loads search-index.json and filters it as the reader types.\nexport async function search(q){const r=await fetch('../search-index.json');const idx=await r.json();return idx.filter(e=>e.title.toLowerCase().includes(q.toLowerCase()));}\n"), by, at)
			add(base+"assets/img/logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32"><rect width="32" height="32" rx="6" fill="#0b63c4"/><text x="16" y="22" font-size="16" text-anchor="middle" fill="#fff">`+strings.ToUpper(prod[:1])+`</text></svg>`+"\n"), by, at)
			add(base+"search-index.json", []byte(fmt.Sprintf(`[{"title":"%s","url":"index.html"},{"title":"Getting started","url":"getting-started/"}]`+"\n", title)), by, at)
			add(base+"sitemap.xml", []byte(fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n  <url><loc>https://docs.example.com/%s/%s/</loc></url>\n</urlset>\n", prod, v)), by, at)
		}
	}

	// Release archives: releases/<yyyy>/<mm>/<product>-<version>/...
	anchor := time.Unix(run.Anchor, 0).UTC()
	productVersion := map[string]int{}
	for m := 23; m >= 0; m-- {
		month := anchor.AddDate(0, -m, 0)
		first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
		for range 1 + rng.IntN(4) {
			prod := demo.Pick(rng, rawProducts)
			productVersion[prod]++
			n := productVersion[prod]
			v := fmt.Sprintf("%d.%d.%d", 1+n/12, (n/3)%4, n%3)
			at := min(first+rng.Int64N(28*demo.Day), run.Anchor-60)
			by := run.Uploader(rng, repo, 3)
			dir := fmt.Sprintf("releases/%04d/%02d/%s-%s/", month.Year(), int(month.Month()), prod, v)
			var sums strings.Builder
			for _, plat := range rawPlatforms {
				name := prod + "-" + v + "-" + plat
				var data []byte
				if strings.HasPrefix(plat, "windows") {
					name += ".zip"
					data = releaseZip(prod, v)
				} else {
					name += ".tar.gz"
					data = releaseTarGz(prod, v)
				}
				s := sha256.Sum256(data)
				fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(s[:]), name)
				add(dir+name, data, by, at)
			}
			add(dir+"SHA256SUMS", []byte(sums.String()), by, at)
			add(dir+"RELEASE_NOTES.md", []byte(fmt.Sprintf("# %s %s\n\n- Faster startup on large data sets.\n- Fixes a crash when the config file is empty.\n- Updates dependencies.\n", prod, v)), by, at)
		}
	}

	// Build logs: build-logs/<project>/<branch>/<build>/..., up to the file budget.
	type branch struct {
		project, name string
		build         int
	}
	var branches []*branch
	seen := map[string]bool{}
	addBranch := func(project, name string, build int) {
		if !seen[project+"/"+name] {
			seen[project+"/"+name] = true
			branches = append(branches, &branch{project, name, build})
		}
	}
	for _, prod := range rawProducts {
		addBranch(prod, "main", 1000+rng.IntN(500))
		for range 1 + rng.IntN(2) {
			addBranch(prod, fmt.Sprintf("release/%d.%d", 1+rng.IntN(2), rng.IntN(4)), 1+rng.IntN(50))
		}
		for range 2 + rng.IntN(4) {
			addBranch(prod, "feature/"+demo.Pick(rng, rawBranchWords)+"-"+demo.Pick(rng, rawBranchWords), 1+rng.IntN(20))
		}
	}
	// About 5 files per build; builds run evenly over the two years, the newest one today.
	builds := int64(max((demoRawFiles-len(out))/5, 1))
	for i := int64(0); len(out) < demoRawFiles; i++ {
		b := branches[rng.IntN(len(branches))]
		b.build++
		at := run.Anchor - 60 - (729*demo.Day)*max(builds-1-i, 0)/builds - rng.Int64N(3600)
		// The logs come from the project's GitHub Actions run, which has no user of this server.
		ghRepo, ref := "harborlabs/"+b.project, "refs/heads/"+b.name
		var err error
		build, err = run.Build(map[string]any{
			"repository": ghRepo, "sha": demo.Hex(rng, 40), "ref": ref,
			"run_id": fmt.Sprint(5_000_000_000 + int64(b.build)*7919 + rng.Int64N(7919)), "run_attempt": "1",
			"workflow": "Build", "workflow_ref": ghRepo + "/.github/workflows/build.yml@" + ref, "actor": demo.Pick(rng, rawActors),
		}, at)
		if err != nil {
			return nil, err
		}
		var by int64
		dir := fmt.Sprintf("build-logs/%s/%s/%d/", b.project, b.name, b.build)
		ok := rng.IntN(10) > 0
		add(dir+"build.log", buildLog(rng, b.project, b.name, b.build, at, ok), by, at)
		for _, mod := range rawModules[:1+rng.IntN(3)] {
			if len(out) >= demoRawFiles {
				break
			}
			add(dir+"test-results/TEST-"+b.project+"."+mod+".xml", junitXML(rng, b.project+"."+mod, at), by, at)
		}
		if len(out) < demoRawFiles {
			add(dir+"coverage/summary.txt", []byte(fmt.Sprintf("lines: %d.%d%%\nbranches: %d.%d%%\n", 60+rng.IntN(35), rng.IntN(10), 50+rng.IntN(40), rng.IntN(10))), by, at)
		}
	}
	return out[:min(len(out), demoRawFiles)], nil
}

func htmlPage(title, body string) []byte {
	return []byte("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>" + title +
		"</title>\n<link rel=\"stylesheet\" href=\"/assets/css/site.css\">\n</head>\n<body>\n<main>\n<h1>" + title +
		"</h1>\n<p>" + body + "</p>\n</main>\n</body>\n</html>\n")
}

func buildLog(rng *rand.Rand, project, branch string, build int, at int64, ok bool) []byte {
	t := time.Unix(at, 0).UTC()
	var b strings.Builder
	line := func(sec int, msg string) {
		fmt.Fprintf(&b, "%s %s\n", t.Add(time.Duration(sec)*time.Second).Format(time.RFC3339), msg)
	}
	line(0, fmt.Sprintf("build #%d of %s (%s) started", build, project, branch))
	line(2, "checkout: "+hex.EncodeToString([]byte(fmt.Sprintf("%s%d", project, build)))[:12])
	line(9, "resolve dependencies: 142 artifacts, 0 downloaded")
	secs := 40 + rng.IntN(400)
	line(secs/2, fmt.Sprintf("compile: %d sources", 80+rng.IntN(900)))
	tests := 100 + rng.IntN(2000)
	if ok {
		line(secs, fmt.Sprintf("test: %d passed, 0 failed", tests))
		line(secs+1, "result: SUCCESS")
	} else {
		line(secs, fmt.Sprintf("test: %d passed, %d failed", tests-3, 3))
		line(secs+1, "result: FAILURE")
	}
	return []byte(b.String())
}

func junitXML(rng *rand.Rand, suite string, at int64) []byte {
	n := 3 + rng.IntN(6)
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuite name=\"%s\" tests=\"%d\" failures=\"0\" errors=\"0\" timestamp=\"%s\">\n", suite, n, time.Unix(at, 0).UTC().Format("2006-01-02T15:04:05"))
	for i := range n {
		fmt.Fprintf(&b, "  <testcase classname=\"%s\" name=\"test%s%d\" time=\"0.%03d\"/>\n", suite, demo.Pick(rng, rawClasses), i, rng.IntN(1000))
	}
	b.WriteString("</testsuite>\n")
	return []byte(b.String())
}

// releaseTarGz is a valid .tar.gz with a README and a start script.
func releaseTarGz(prod, v string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := prod + "-" + v + "/"
	for _, f := range []struct {
		name string
		mode int64
		body string
	}{
		{dir + "README", 0o644, prod + " " + v + "\n"},
		{dir + "bin/" + prod, 0o755, "#!/bin/sh\necho \"" + prod + " " + v + "\"\n"},
	} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), ModTime: demo.ZipTime, Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// releaseZip is a valid .zip with a README and a start script.
func releaseZip(prod, v string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	dir := prod + "-" + v + "/"
	for _, f := range [][2]string{{dir + "README.txt", prod + " " + v + "\r\n"}, {dir + path.Join("bin", prod+".cmd"), "@echo " + prod + " " + v + "\r\n"}} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f[0], Method: zip.Deflate, Modified: demo.ZipTime})
		if err != nil {
			panic(err) // writing to a bytes.Buffer does not fail
		}
		w.Write([]byte(f[1]))
	}
	zw.Close()
	return buf.Bytes()
}
