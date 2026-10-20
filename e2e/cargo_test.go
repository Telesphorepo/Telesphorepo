//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// cargoIndexLines reads the index file of crate in repo with auth and returns its lines by
// version. A missing file is an empty map.
func cargoIndexLines(t *testing.T, s *Server, repo, crate string, auth Auth) map[string]string {
	t.Helper()
	n := strings.ToLower(crate)
	var p string
	switch len(n) {
	case 1:
		p = "1/" + n
	case 2:
		p = "2/" + n
	case 3:
		p = "3/" + n[:1] + "/" + n
	default:
		p = n[:2] + "/" + n[2:4] + "/" + n
	}
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/cargo/"+repo+"/index/"+p, nil), auth)
	out := map[string]string{}
	if resp.StatusCode == http.StatusNotFound {
		return out
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET the index file of %s in %s: status %d: %s", crate, repo, resp.StatusCode, b)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatalf("the index file of %s in %s has no ETag", crate, repo)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e struct {
			Vers string `json:"vers"`
		}
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("index line of %s: %v: %s", crate, err, l)
		}
		out[e.Vers] = l
	}
	return out
}

// cargoEntry is the part of an index line that the tests read.
type cargoEntry struct {
	Name   string `json:"name"`
	Vers   string `json:"vers"`
	Yanked bool   `json:"yanked"`
	Deps   []struct {
		Name     string  `json:"name"`
		Req      string  `json:"req"`
		Kind     string  `json:"kind"`
		Registry *string `json:"registry"`
		Package  *string `json:"package"`
		Target   *string `json:"target"`
	} `json:"deps"`
	Features2 map[string][]string `json:"features2"`
	V         int                 `json:"v"`
}

func parseCargoEntry(t *testing.T, line string) cargoEntry {
	t.Helper()
	var e cargoEntry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatalf("index line: %v: %s", err, line)
	}
	return e
}

// TestCargo publishes crates with cargo publish to a private hosted repo (auth-required), yanks
// and unyanks a version with cargo yank, builds a binary that depends on the crate, and checks
// that the entry of a publish equals the entry that an upload of the same .crate gets from its
// Cargo.toml.
func TestCargo(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo, other := RandName("crates"), RandName("crates2")
		s.CreateRepo(t, repo, "cargo", false, nil)
		s.CreateRepo(t, other, "cargo", false, nil)
		dev, devPass := RandName("dev"), randHex(12)
		s.CreateUser(t, dev, devPass)
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		read := s.CreateToken(t, dev, devPass, "cargo-e2e")
		suffix := randHex(4)
		crate, base := "e2e_"+suffix, "e2eb_"+suffix
		index := s.InternalURL + "/cargo/" + repo + "/index/"

		// A private registry answers config.json without a token with 401 and a Cargo challenge.
		resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/cargo/"+repo+"/index/config.json", nil), nil)
		if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Cargo login_url=") {
			t.Fatalf("anonymous config.json: status %d, WWW-Authenticate %q, want 401 with a Cargo challenge: %s",
				resp.StatusCode, resp.Header.Get("WWW-Authenticate"), b)
		}
		resp, b = s.Do(t, s.NewRequest(t, http.MethodGet, "/cargo/"+repo+"/index/config.json", nil), rawToken(read))
		var cfg struct {
			DL           string `json:"dl"`
			API          string `json:"api"`
			AuthRequired bool   `json:"auth-required"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &cfg) != nil || !cfg.AuthRequired ||
			cfg.DL != s.InternalURL+"/cargo/"+repo+"/crates/{crate}/{crate}-{version}.crate" || cfg.API != s.InternalURL+"/cargo/"+repo {
			t.Fatalf("config.json with a read token: status %d: %s", resp.StatusCode, b)
		}

		// The main crate depends on crates.io, on a renamed crate of this registry, on an optional
		// crate behind a dep: feature, on a crate for one target, and has a dev dependency.
		extra := func(ver string) string {
			return "rust-version = \"1.70\"\n\n[dependencies]\ncfg-if = \"1\"\n" +
				"base = { package = \"" + base + "\", version = \"0.1\", registry = \"tr\" }\n" +
				"itoa = { version = \"1.0\", optional = true, default-features = false }\n\n" +
				"[target.'cfg(target_os = \"linux\")'.dependencies]\nmemchr = { version = \"2\", default-features = false }\n\n" +
				"[dev-dependencies]\ncfg-if = \"1.0\"\n\n[features]\ndefault = []\nfast = [\"dep:itoa\"]\n"
		}
		files := []ContainerFile{cargoRegistry("/work", "tr", index, false)}
		files = append(files, cargoLib("/work/base", base, "0.1.0", "", "base of "+suffix)...)
		files = append(files, cargoLib("/work/lib", crate, "1.0.0", extra("1.0.0"), "hello 1.0.0 from "+suffix)...)
		files = append(files, cargoLib("/work/clash", strings.ReplaceAll(crate, "_", "-"), "1.0.0", "", "clash")...)
		script := `set -e
cd /work/base && cargo publish --registry tr
cd /work/lib && cargo publish --registry tr
sed -i 's/^version = "1.0.0"/version = "1.1.0"/; s/hello 1.0.0/hello 1.1.0/' Cargo.toml src/lib.rs
cargo publish --registry tr
# The crate file that the test compares: cargo package writes the same bytes to target/package.
cargo package --registry tr --no-verify
if cargo publish --registry tr --no-verify 2>/tmp/again.txt; then echo "REPUBLISH ACCEPTED"; exit 1; fi
cat /tmp/again.txt
cd /work/clash
if cargo publish --registry tr --no-verify 2>/tmp/clash.txt; then echo "CLASH ACCEPTED"; exit 1; fi
cat /tmp/clash.txt
`
		res := RunClientResult(t, rustImage, ClientOpts{Cmd: []string{script}, Files: files, Timeout: 20 * time.Minute,
			Env: cargoEnv(map[string]string{cargoTokenEnv("tr"): PublishToken(t, s, dev, repo)})})
		if res.ExitCode != 0 {
			t.Fatalf("cargo publish: exit %d\n%s", res.ExitCode, res.Output)
		}
		if !strings.Contains(res.Output, "already") {
			t.Fatalf("republish of 1.1.0 failed without saying it is already uploaded:\n%s", res.Output)
		}
		if !strings.Contains(res.Output, "clashes with the crate "+crate) {
			t.Fatalf("publish of a clashing name failed without naming the clash:\n%s", res.Output)
		}
		crateFile := res.ReadFile(t, "/work/lib/target/package/"+crate+"-1.1.0.crate")

		lines := cargoIndexLines(t, s, repo, crate, s.AdminBasic())
		if len(lines) != 2 || lines["1.0.0"] == "" || lines["1.1.0"] == "" {
			t.Fatalf("the index of %s lists %v, want 1.0.0 and 1.1.0", crate, lines)
		}
		e := parseCargoEntry(t, lines["1.1.0"])
		if e.V != 2 || len(e.Features2["fast"]) != 1 {
			t.Fatalf("the entry of 1.1.0 has v %d and features2 %v, want v 2 with fast: %s", e.V, e.Features2, lines["1.1.0"])
		}
		for _, d := range e.Deps {
			switch {
			case d.Name == "base" && (d.Package == nil || *d.Package != base || d.Registry != nil || d.Req != "^0.1"):
				t.Fatalf("the renamed dependency of this registry: %s", lines["1.1.0"])
			case d.Name == "cfg-if" && (d.Registry == nil || *d.Registry != "https://github.com/rust-lang/crates.io-index"):
				t.Fatalf("the crates.io dependency names no crates.io registry: %s", lines["1.1.0"])
			case d.Name == "memchr" && (d.Target == nil || *d.Target != `cfg(target_os = "linux")`):
				t.Fatalf("the target dependency: %s", lines["1.1.0"])
			}
		}

		// Yank and unyank 1.0.0 with cargo; a read token cannot yank.
		resp, b = s.Do(t, s.NewRequest(t, http.MethodDelete, "/cargo/"+repo+"/api/v1/crates/"+crate+"/1.0.0/yank", nil), rawToken(read))
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(b), `"errors"`) || !strings.Contains(string(b), "publish session") {
			t.Fatalf("yank with a read token: status %d, want 401 with a Cargo error: %s", resp.StatusCode, b)
		}
		yank := func(undo bool) {
			t.Helper()
			cmd := "cargo yank --registry tr " + crate + "@1.0.0"
			if undo {
				cmd = "cargo yank --undo --registry tr " + crate + "@1.0.0"
			}
			code, out := RunClient(t, rustImage, ClientOpts{Cmd: []string{"cd /work && " + cmd}, Files: []ContainerFile{cargoRegistry("/work", "tr", index, false)},
				Env: cargoEnv(map[string]string{cargoTokenEnv("tr"): PublishToken(t, s, dev, repo)})})
			if code != 0 {
				t.Fatalf("%s: exit %d\n%s", cmd, code, out)
			}
			if got := parseCargoEntry(t, cargoIndexLines(t, s, repo, crate, s.AdminBasic())["1.0.0"]).Yanked; got != !undo {
				t.Fatalf("after %s the index says yanked %v", cmd, got)
			}
		}
		yank(false)

		// A build with the read token resolves the newest version; the yanked one stays out.
		consumer := append(cargoBin("/work/app", crate+" = { version = \"1\", registry = \"tr\" }\n", crate),
			cargoRegistry("/work", "tr", index, false))
		code, out := RunClient(t, rustImage, ClientOpts{Cmd: []string{"cd /work/app && cargo run -q"}, Files: consumer,
			Env: cargoEnv(map[string]string{cargoTokenEnv("tr"): read}), Timeout: 20 * time.Minute})
		if code != 0 || !strings.Contains(out, "hello 1.1.0 from "+suffix) {
			t.Fatalf("cargo run of a consumer: exit %d\n%s", code, out)
		}
		yank(true)

		// The entry of the publish equals the entry of an upload of the same .crate: delete the
		// version, then upload its .crate through the web UI path.
		published := lines["1.1.0"]
		path := "crates/" + crate + "/" + crate + "-1.1.0.crate"
		mustDelete(t, s, repo, "path", path, s.Admin(), 1)
		if l := cargoIndexLines(t, s, repo, crate, s.AdminBasic()); l["1.1.0"] != "" || l["1.0.0"] == "" {
			t.Fatalf("after the delete of 1.1.0 the index lists %v", l)
		}
		up := mustUpload(t, s, repo, s.MintSession(t, s.AdminUser, repo).Auth(), nil, uploadPart{Name: crate + "-1.1.0.crate", Body: crateFile})
		if len(up) != 1 || up[0].Path != path {
			t.Fatalf("upload of the .crate: %+v, want %s", up, path)
		}
		if got := cargoIndexLines(t, s, repo, crate, s.AdminBasic())["1.1.0"]; got != published {
			t.Fatalf("the entry of the upload differs from the entry of the publish:\nupload:  %s\npublish: %s", got, published)
		}

		// In another repo, the dependency of the first repo names that repo's index.
		mustUpload(t, s, other, s.MintSession(t, s.AdminUser, other).Auth(), nil, uploadPart{Name: crate + "-1.1.0.crate", Body: crateFile})
		for _, d := range parseCargoEntry(t, cargoIndexLines(t, s, other, crate, s.AdminBasic())["1.1.0"]).Deps {
			if d.Name == "base" && (d.Registry == nil || *d.Registry != "sparse+"+index) {
				t.Fatalf("the dependency base of the upload to %s names registry %v, want sparse+%s", other, d.Registry, index)
			}
		}
	})
}

// TestCargoProxyGroup builds from a crates.io proxy through source replacement, and from a private
// group of a hosted repo and the proxy, in which a hosted crate named like a crates.io crate is
// not shadowed by the higher public versions.
func TestCargoProxyGroup(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		hosted, proxy, group := RandName("crates"), RandName("crates-io"), RandName("cargo-all")
		s.CreateRepo(t, hosted, "cargo", false, nil)
		s.CreateProxy(t, proxy, "cargo", true, map[string]any{"remote_url": "https://index.crates.io/"}, false)
		s.CreateGroup(t, group, "cargo", false, []string{hosted, proxy})
		dev, devPass := RandName("dev"), randHex(12)
		s.CreateUser(t, dev, devPass)
		s.SetPerms(t, hosted, dev, "repo.download", "repo.publish")
		s.SetPerms(t, group, dev, "repo.download")
		read := s.CreateToken(t, dev, devPass, "cargo-group")
		suffix := randHex(4)
		private := "e2ep_" + suffix

		// The hosted repo holds its own crate and a crate named itoa, which crates.io has in
		// higher versions.
		files := []ContainerFile{cargoRegistry("/work", "tr", s.InternalURL+"/cargo/"+hosted+"/index/", false)}
		files = append(files, cargoLib("/work/p", private, "0.1.0", "", "private "+suffix)...)
		files = append(files, cargoLib("/work/itoa", "itoa", "0.0.1", "", "the private itoa")...)
		code, out := RunClient(t, rustImage, ClientOpts{
			Cmd:   []string{"set -e\ncd /work/p && cargo publish --registry tr\ncd /work/itoa && cargo publish --registry tr"},
			Files: files, Env: cargoEnv(map[string]string{cargoTokenEnv("tr"): PublishToken(t, s, dev, hosted)}), Timeout: 20 * time.Minute})
		if code != 0 {
			t.Fatalf("cargo publish to the hosted repo: exit %d\n%s", code, out)
		}

		// Through the proxy alone: crates.io crates, by source replacement, without a token.
		code, out = RunClient(t, rustImage, ClientOpts{Cmd: []string{"cd /work/app && cargo build -q && echo BUILT"},
			Files: append(cargoBin("/work/app", "itoa = \"1\"\ncfg-if = \"1\"\n"), cargoRegistry("/work", "tp", s.InternalURL+"/cargo/"+proxy+"/index/", true)),
			Env:   cargoEnv(nil), Timeout: 20 * time.Minute})
		if code != 0 || !strings.Contains(out, "BUILT") {
			t.Fatalf("cargo build through the proxy: exit %d\n%s", code, out)
		}
		paths := assetPathsOf(t, s, proxy, "")
		for _, want := range []string{".index/it/oa/itoa", ".index/cf/g-/cfg-if"} {
			if !strings.Contains(strings.Join(paths, "\n"), want) {
				t.Fatalf("the proxy keeps no %s: %v", want, paths)
			}
		}
		if !strings.Contains(strings.Join(paths, "\n"), "crates/itoa/itoa-1.") {
			t.Fatalf("the proxy keeps no itoa 1.x crate: %v", paths)
		}

		// The group: the hosted itoa owns the name, so only 0.0.1 is listed.
		if l := cargoIndexLines(t, s, group, "itoa", rawToken(read)); len(l) != 1 || l["0.0.1"] == "" {
			t.Fatalf("the group's index of itoa lists %d versions, want only the hosted 0.0.1", len(l))
		}
		if l := cargoIndexLines(t, s, group, "cfg-if", rawToken(read)); len(l) < 2 {
			t.Fatalf("the group's index of cfg-if lists %d versions, want those of crates.io", len(l))
		}
		resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/cargo/"+group+"/index/config.json", nil), nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous config.json of the private group: status %d, want 401", resp.StatusCode)
		}

		// A build through the private group with the read token: the hosted crate and a crates.io
		// crate, both as crates.io crates by source replacement.
		code, out = RunClient(t, rustImage, ClientOpts{Cmd: []string{"cd /work/app && cargo run -q"},
			Files: append(cargoBin("/work/app", private+" = \"0.1\"\ncfg-if = \"1\"\n", private), cargoRegistry("/work", "tg", s.InternalURL+"/cargo/"+group+"/index/", true)),
			Env:   cargoEnv(map[string]string{cargoTokenEnv("tg"): read}), Timeout: 20 * time.Minute})
		if code != 0 || !strings.Contains(out, "private "+suffix) {
			t.Fatalf("cargo run through the group: exit %d\n%s", code, out)
		}
	})
}
