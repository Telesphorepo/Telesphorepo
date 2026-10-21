//go:build e2e

package e2e

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// npmDoc is the part of a packument that the tests read.
type npmDoc struct {
	Name     string            `json:"name"`
	Rev      string            `json:"_rev"`
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Deprecated string `json:"deprecated"`
		Dist       struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
			Shasum    string `json:"shasum"`
		} `json:"dist"`
	} `json:"versions"`
}

// npmGet GETs the packument of name in repo (the path form of name as given) and decodes it.
func npmGet(t *testing.T, s *Server, repo, name string, abbreviated bool, auth Auth) (*http.Response, *npmDoc) {
	t.Helper()
	req := s.NewRequest(t, http.MethodGet, "/npm/"+repo+"/"+name, nil)
	if abbreviated {
		req.Header.Set("Accept", "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*")
	}
	resp, b := s.Do(t, req, auth)
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	var d npmDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("GET packument %s: %v: %s", name, err, b)
	}
	return resp, &d
}

// npmMust runs script in image and fails the test on an exit code other than 0.
func npmMust(t *testing.T, image, what, script string, files []ContainerFile) string {
	t.Helper()
	code, out := RunClient(t, image, ClientOpts{Cmd: []string{script}, Files: files, Timeout: 15 * time.Minute})
	if code != 0 {
		t.Fatalf("%s: exit %d:\n%s", what, code, out)
	}
	return out
}

// TestNPM publishes a plain and a scoped package with npm publish and installs both with npm,
// pnpm, Yarn and Bun from the hosted repo with a read token; a second publish of a version is
// refused with 403. The packument names the server's tarball URL with the integrity of the
// stored bytes, answers 304 to its ETag and serves both forms of a scoped name.
func TestNPM(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("npm")
		s.CreateRepo(t, repo, "npm", false, nil)
		writer, pass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, pass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		read := s.CreateToken(t, writer, pass, "npm-e2e")
		auth := Basic{writer, pass}
		registry := s.InternalURL + "/npm/" + repo + "/"
		plain, scoped := RandName("plain"), "@e2e/"+RandName("lib")

		files := append(npmPackage("/work/plain", plain, "1.0.0", "plain 1.0.0"), npmPackage("/work/scoped", scoped, "1.0.0", "scoped 1.0.0")...)
		files = append(files, ContainerFile{Path: "/root/.npmrc", Content: npmrc(registry, PublishToken(t, s, writer, repo))})
		out := npmMust(t, nodeImage, "npm publish", `set -e
cd /work/plain && npm publish
cd /work/scoped && npm publish --access public
npm whoami
if npm publish 2>/tmp/again.log; then echo REPUBLISH_ACCEPTED; else echo REPUBLISH_REFUSED; cat /tmp/again.log; fi
if npm publish --force 2>/tmp/forced.log; then echo FORCED_ACCEPTED; else echo FORCED_REFUSED; cat /tmp/forced.log; fi
`, files)
		// npm refuses a version that the packument lists before it sends anything; --force skips
		// that check, and the server answers 403.
		npmWant(t, "npm publish", out, writer, "REPUBLISH_REFUSED", "You cannot publish over the previously published versions: 1.0.0",
			"FORCED_REFUSED", "403", "version 1.0.0 of "+scoped+" already exists")

		for _, name := range []string{plain, scoped, "@e2e%2f" + strings.TrimPrefix(scoped, "@e2e/")} {
			resp, d := npmGet(t, s, repo, name, true, auth)
			if d == nil || resp.Header.Get("Content-Type") != "application/vnd.npm.install-v1+json" || d.DistTags["latest"] != "1.0.0" {
				t.Fatalf("abbreviated packument of %s: status %d, type %q, %+v", name, resp.StatusCode, resp.Header.Get("Content-Type"), d)
			}
			v := d.Versions["1.0.0"]
			pkg, _ := url.PathUnescape(name)
			wantTarball := "/npm/" + repo + "/" + pkg + "/-/" + pkg[strings.LastIndex(pkg, "/")+1:] + "-1.0.0.tgz"
			if !strings.HasSuffix(v.Dist.Tarball, wantTarball) || !strings.HasPrefix(v.Dist.Integrity, "sha512-") || len(v.Dist.Shasum) != 40 {
				t.Fatalf("dist of %s: %+v, want a tarball ending in %s", name, v.Dist, wantTarball)
			}
			u, _ := url.Parse(v.Dist.Tarball)
			tgz := getFollowS3(t, s, opts.S3, u.Path)
			sum := sha512.Sum512(tgz)
			if "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != v.Dist.Integrity {
				t.Fatalf("tarball of %s: integrity of the bytes differs from %s", name, v.Dist.Integrity)
			}
			req := s.NewRequest(t, http.MethodGet, "/npm/"+repo+"/"+name, nil)
			req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
			req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
			if r304, _ := s.Do(t, req, auth); r304.StatusCode != http.StatusNotModified {
				t.Fatalf("packument of %s with its ETag: status %d, want 304", name, r304.StatusCode)
			}
		}
		if resp, d := npmGet(t, s, repo, plain, false, auth); d == nil || d.Rev == "" || resp.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("full packument of %s: status %d, %+v", plain, resp.StatusCode, d)
		}
		if resp, _ := npmGet(t, s, repo, plain, true, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous packument of a private repo: status %d, want 401", resp.StatusCode)
		}

		host := s.Alias
		deps := map[string]string{plain: "^1.0.0", scoped: "1.0.0"}
		install := []ContainerFile{
			npmConsumer("/work/npm", deps), {Path: "/work/npm/.npmrc", Content: npmrc(registry, read)},
			npmConsumer("/work/pnpm", deps), {Path: "/work/pnpm/.npmrc", Content: npmrc(registry, read)},
			npmConsumer("/work/yarn", deps), {Path: "/work/yarn/.yarnrc.yml", Content: yarnrc(registry, host, read)},
		}
		out = npmMust(t, nodeImage, "install with npm, pnpm and yarn", "set -e\n"+npmSetupTools+
			"cd /work/npm && npm install --no-audit --no-fund\n"+npmRequire(plain, scoped)+
			"cd /work/pnpm && pnpm install\n"+npmRequire(plain, scoped)+
			"cd /work/yarn && yarn install\n"+npmRequire(plain, scoped), install)
		if strings.Count(out, "REQ "+plain+": plain 1.0.0") != 3 || strings.Count(out, "REQ "+scoped+": scoped 1.0.0") != 3 {
			t.Fatalf("install with npm, pnpm and yarn: want each package three times:\n%s", out)
		}
		out = npmMust(t, bunImage, "install with bun", "set -e\ncd /work/bun && bun install\n"+
			strings.ReplaceAll(npmRequire(plain, scoped), "node -e", "bun -e"),
			[]ContainerFile{npmConsumer("/work/bun", deps), {Path: "/work/bun/.npmrc", Content: npmrc(registry, read)}})
		npmWant(t, "install with bun", out, "REQ "+plain+": plain 1.0.0", "REQ "+scoped+": scoped 1.0.0")

		// A new version shows in the packument at once: the packument of before no longer answers
		// 304 to its ETag.
		resp, _ := npmGet(t, s, repo, plain, true, auth)
		before := resp.Header.Get("ETag")
		npmMust(t, nodeImage, "npm publish of 1.1.0", "set -e\ncd /work/plain && npm publish\n",
			append(npmPackage("/work/plain", plain, "1.1.0", "plain 1.1.0"),
				ContainerFile{Path: "/root/.npmrc", Content: npmrc(registry, PublishToken(t, s, writer, repo))}))
		if _, d := npmGet(t, s, repo, plain, true, auth); d == nil || d.DistTags["latest"] != "1.1.0" || len(d.Versions) != 2 {
			t.Fatalf("abbreviated packument of %s after the publish of 1.1.0: %+v, want 1.0.0 and 1.1.0 with latest 1.1.0", plain, d)
		}
		if st, etag := conditionalGet(t, s, "/npm/"+repo+"/"+plain, "application/vnd.npm.install-v1+json", before, auth); st != http.StatusOK || etag == before {
			t.Fatalf("packument after the publish of 1.1.0 with the ETag from before: status %d, ETag %s, want 200 and a new ETag", st, etag)
		}
	})
}

// TestNPMRegistry covers the rest on the local disk: a proxy of registry.npmjs.org and a group of
// the hosted repo and the proxy, from which npm, pnpm, Yarn and Bun install; a private name in the
// hosted member is not shadowed by the higher public versions of the proxy; npm dist-tag add, ls
// and rm; npm deprecate; npm unpublish of a version with a device session, refused with a CI
// session, which still sets a dist-tag.
func TestNPMRegistry(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		hosted, proxy, group := RandName("npm"), RandName("npmjs"), RandName("npmall")
		s.CreateRepo(t, hosted, "npm", false, nil)
		s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{"name": proxy, "format": "npm", "anonymous_read": true,
			"kind": "proxy", "proxy": map[string]any{"remote_url": "https://registry.npmjs.org/"}, "force": true}, s.Admin(), nil)
		s.CreateGroup(t, group, "npm", false, []string{hosted, proxy})
		writer, pass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, pass)
		for _, r := range []string{hosted, group} {
			s.SetPerms(t, r, writer, "repo.download", "repo.publish", "repo.delete")
		}
		read := s.CreateToken(t, writer, pass, "npm-e2e")
		auth := Basic{writer, pass}
		reg := func(r string) string { return s.InternalURL + "/npm/" + r + "/" }
		tools := RandName("tools")

		// is-number is a public package (7.x on registry.npmjs.org); the hosted member has its own 1.0.0.
		files := append(npmPackage("/work/is-number", "is-number", "1.0.0", "private is-number"), npmPackage("/work/t1", tools, "1.0.0", "tools 1.0.0")...)
		files = append(files, npmPackage("/work/t2", tools, "1.1.0", "tools 1.1.0")...)
		files = append(files, ContainerFile{Path: "/root/.npmrc", Content: npmrc(reg(hosted), PublishToken(t, s, writer, hosted))})
		out := npmMust(t, nodeImage, "publish, dist-tag, deprecate and unpublish", `set -e
for d in is-number t1 t2; do (cd /work/$d && npm publish); done
npm dist-tag add `+tools+`@1.0.0 stable
echo '=== tags after add ==='
npm dist-tag ls `+tools+`
npm dist-tag rm `+tools+` stable
echo '=== tags after rm ==='
npm dist-tag ls `+tools+`
echo '=== end ==='
npm deprecate `+tools+`@1.0.0 "use 1.1.0"
npm unpublish `+tools+`@1.1.0
`, files)
		if add, rm := npmSection(out, "tags after add"), npmSection(out, "tags after rm"); !strings.Contains(add, "stable: 1.0.0") || !strings.Contains(add, "latest: 1.1.0") ||
			strings.Contains(rm, "stable") || !strings.Contains(rm, "latest") {
			t.Fatalf("npm dist-tag: after add %q, after rm %q", add, rm)
		}
		_, d := npmGet(t, s, hosted, tools, false, auth)
		if d == nil || d.Versions["1.0.0"].Deprecated != "use 1.1.0" || len(d.Versions) != 1 || d.DistTags["latest"] != "1.0.0" {
			t.Fatalf("after deprecate and unpublish: %+v, want 1.0.0 only, deprecated, and latest 1.0.0", d)
		}
		if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/npm/"+hosted+"/"+tools+"/-/"+tools+"-1.1.0.tgz", nil), auth); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("tarball of an unpublished version: status %d, want 404", resp.StatusCode)
		}

		// A CI session writes (a dist-tag) but does not delete.
		kc := newKeycloak(t)
		kc.Start(t, s.InternalURL+"/api/auth/oidc/callback")
		s.MustAPI(t, http.MethodPost, "/api/publish/trusts", map[string]any{"name": RandName("ci"), "issuer": kc.Issuer,
			"audience": "telesphorepo-e2e-ci", "claims": map[string][]string{"azp": {"e2e-ci"}}, "repos": []string{hosted}}, s.Admin(), nil)
		form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"e2e-ci"}, "client_secret": {"e2e-ci-client-secret"}}
		resp, err := kc.Client(nil).PostForm(kc.Issuer+"/protocol/openid-connect/token", form)
		if err != nil {
			t.Fatalf("CI token from keycloak: %v", err)
		}
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		json.NewDecoder(resp.Body).Decode(&tok)
		resp.Body.Close()
		ci := mustSession(t, "CI session")(s.OIDCSession(t, tok.AccessToken, hosted))
		code, out := RunClient(t, nodeImage, ClientOpts{Cmd: []string{`npm dist-tag add ` + tools + `@1.0.0 ci && echo TAG_SET
npm unpublish ` + tools + `@1.0.0 --force && echo UNPUBLISHED`},
			Files: []ContainerFile{{Path: "/root/.npmrc", Content: npmrc(reg(hosted), ci.Token)}}, Timeout: 10 * time.Minute})
		if code == 0 || !strings.Contains(out, "TAG_SET") || strings.Contains(out, "UNPUBLISHED") {
			t.Fatalf("CI session: exit %d, want the tag set and the unpublish refused:\n%s", code, out)
		}
		if _, d := npmGet(t, s, hosted, tools, true, auth); d == nil || len(d.Versions) != 1 || d.DistTags["ci"] != "1.0.0" {
			t.Fatalf("after the CI session: %+v, want 1.0.0 with the tag ci", d)
		}

		// The group serves is-number from the hosted member only.
		if _, d := npmGet(t, s, group, "is-number", true, auth); d == nil || len(d.Versions) != 1 || d.DistTags["latest"] != "1.0.0" {
			t.Fatalf("is-number in the group: %+v, want the hosted 1.0.0 only", d)
		}
		if _, d := npmGet(t, s, proxy, "is-number", true, nil); d == nil || len(d.Versions) < 5 || !strings.HasPrefix(d.Versions["7.0.0"].Dist.Tarball, "http") ||
			!strings.Contains(d.Versions["7.0.0"].Dist.Tarball, "/npm/"+proxy+"/is-number/-/is-number-7.0.0.tgz") {
			t.Fatalf("is-number in the proxy: %+v, want the public versions with tarballs on the server", d)
		}

		host := s.Alias
		pub := map[string]string{"ms": "2.1.3", "@types/ms": "0.7.34"}
		mixed := map[string]string{"ms": "2.1.3", "@types/ms": "0.7.34", "is-number": "*", tools: "^1.0.0"}
		var install []ContainerFile
		for _, k := range []struct {
			dir, registry, token string
			deps                 map[string]string
		}{{"proxy", reg(proxy), "", pub}, {"group", reg(group), read, mixed}} {
			install = append(install,
				npmConsumer("/work/npm-"+k.dir, k.deps), ContainerFile{Path: "/work/npm-" + k.dir + "/.npmrc", Content: npmrc(k.registry, k.token)},
				npmConsumer("/work/pnpm-"+k.dir, k.deps), ContainerFile{Path: "/work/pnpm-" + k.dir + "/.npmrc", Content: npmrc(k.registry, k.token)},
				npmConsumer("/work/yarn-"+k.dir, k.deps), ContainerFile{Path: "/work/yarn-" + k.dir + "/.yarnrc.yml", Content: yarnrc(k.registry, host, k.token)},
			)
		}
		check := npmRequire("ms") + "test -f node_modules/@types/ms/package.json && echo TYPES_OK\n"
		groupCheck := check + npmRequire("is-number", tools)
		// The first pnpm run is in the project of the proxy, so pnpm downloads its binary through it.
		out = npmMust(t, nodeImage, "install from the proxy and the group", "set -e\n"+npmInstallTools+
			"cd /work/npm-proxy && npm install --no-audit --no-fund\n"+check+
			"cd /work/pnpm-proxy && pnpm install\n"+check+
			"cd /work/yarn-proxy && yarn install\n"+check+
			"cd /work/npm-group && npm install --no-audit --no-fund\n"+groupCheck+
			"cd /work/pnpm-group && pnpm install\n"+groupCheck+
			"cd /work/yarn-group && yarn install\n"+groupCheck, install)
		if strings.Count(out, "TYPES_OK") != 6 || strings.Count(out, "REQ is-number: private is-number") != 3 || strings.Count(out, "REQ "+tools+": tools 1.0.0") != 3 {
			t.Fatalf("install from the proxy and the group:\n%s", out)
		}
		if paths := assetPathsOf(t, s, proxy, "@pnpm/"); !strings.Contains(out, "Downloading the pnpm") || len(paths) == 0 {
			t.Fatalf("pnpm did not download its binary through the proxy (cached %v):\n%s", paths, out)
		}
		bunCheck := func(c string) string { return strings.ReplaceAll(c, "node -e", "bun -e") }
		out = npmMust(t, bunImage, "bun install from the proxy and the group", "set -e\n"+
			"cd /work/proxy && bun install\n"+bunCheck(check)+
			"cd /work/group && bun install\n"+bunCheck(groupCheck), []ContainerFile{
			npmConsumer("/work/proxy", pub), {Path: "/work/proxy/.npmrc", Content: npmrc(reg(proxy), "")},
			npmConsumer("/work/group", mixed), {Path: "/work/group/.npmrc", Content: npmrc(reg(group), read)},
		})
		if strings.Count(out, "TYPES_OK") != 2 || !strings.Contains(out, "REQ is-number: private is-number") {
			t.Fatalf("bun install from the proxy and the group:\n%s", out)
		}
		if paths := assetPathsOf(t, s, proxy, "ms/-/"); len(paths) == 0 {
			t.Fatalf("the proxy cached no tarball of ms")
		}
	})
}

// npmWant fails unless text holds each of subs.
func npmWant(t *testing.T, what, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(text, s) {
			t.Fatalf("%s: no %q in:\n%s", what, s, text)
		}
	}
}

// npmSection returns the output between the line "=== name ===" and the next "=== " line.
func npmSection(out, name string) string {
	_, rest, _ := strings.Cut(out, "=== "+name+" ===")
	if i := strings.Index(rest, "\n=== "); i >= 0 {
		rest = rest[:i+1]
	}
	return rest
}

// assetPathsOf lists the asset paths of repo under prefix as the admin.
func assetPathsOf(t *testing.T, s *Server, repo, prefix string) []string {
	t.Helper()
	return mavenAssetPaths(t, s, repo, prefix, s.Admin())
}
