//go:build clients

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"testing"
)

// fakeMvnw is the Maven wrapper of the CI project: it shows what the env of the child holds, and
// uploads one file with TELESPHOREPO_TOKEN to the deployment repository of its arguments, as
// mvn deploy does. publish runs the wrapper of a project in place of mvn.
const fakeMvnw = `#!/bin/sh
set -e
echo "child: OIDC_TOKEN=${OIDC_TOKEN:-unset}"
if [ -n "$TELESPHOREPO_TOKEN" ]; then echo 'child: TELESPHOREPO_TOKEN set'; fi
url=''
for a in "$@"; do
	case "$a" in -DaltDeploymentRepository=telesphorepo::*) url="${a#-DaltDeploymentRepository=telesphorepo::}" ;; esac
done
[ -n "$url" ] || { echo "no deployment repository in: $*"; exit 3; }
printf 'built in CI\n' > /tmp/ci.txt
curl -fsS -u "telesphorepo:$TELESPHOREPO_TOKEN" -T /tmp/ci.txt "$url/com/acme/ci/$E2E_VERSION/ci-$E2E_VERSION.txt"
`

// TestCLICI: in CI, publish mints the session with the identity token of the job, from the
// GitHub Actions token endpoint (a jwebserver that serves a Keycloak token) or from OIDC_TOKEN
// (Jenkins); the files get the build record of the token; the child process never sees
// OIDC_TOKEN; a GitHub Actions job without id-token: write gets the message.
func TestCLICI(t *testing.T) {
	c := newCLI(t)
	kc := newKeycloak(t)
	kc.Start(t, "http://callback.invalid"+oidcCallbackPath)
	gh := startGitHubTokenServer(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("ci")
		s.CreateRepo(t, repo, "maven", false, nil)
		createCITrust(t, s, kc.Issuer, repo)
		files := []ContainerFile{projectFile(s, "/work/app", repo),
			{Path: "/work/app/pom.xml", Content: "<project/>\n"},
			{Path: "/work/app/mvnw", Content: fakeMvnw, Mode: 0o755}}
		publish := func(what string, env map[string]string) (int, string) {
			t.Helper()
			return c.run(t, curlImage, what, "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: env})
		}

		code, out := publish("GitHub Actions without id-token: write", map[string]string{"GITHUB_ACTIONS": "true"})
		if code != 1 {
			t.Fatalf("publish in GitHub Actions without an ID token: exit %d, want 1:\n%s", code, out)
		}
		wantText(t, "publish in GitHub Actions without an ID token", out, "permissions: id-token: write")

		tok := kc.CIToken(t)
		out = c.mustRun(t, curlImage, "Jenkins", "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: map[string]string{
			"JENKINS_URL": "http://jenkins.example/", "OIDC_TOKEN": tok, "E2E_VERSION": "1.0", "E2E_SECRET": tok}})
		wantText(t, "publish in Jenkins", out, "Publishing to "+repo+" (maven)", "child: OIDC_TOKEN=unset", "child: TELESPHOREPO_TOKEN set")

		tok = kc.CIToken(t)
		gh.Serve(t, tok)
		before := gh.Requests(t)
		env := gh.Env()
		env["OIDC_TOKEN"] = "an-identity-token-of-another-ci"
		env["E2E_VERSION"] = "2.0"
		env["E2E_SECRET"] = tok
		out = c.mustRun(t, curlImage, "GitHub Actions", "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: env})
		wantText(t, "publish in GitHub Actions", out, "child: OIDC_TOKEN=unset", "::add-mask::")
		if gh.Requests(t) <= before {
			t.Fatal("publish in GitHub Actions never fetched the ID token from ACTIONS_ID_TOKEN_REQUEST_URL")
		}
		// The CLI masks each token before the child starts: no token appears without its mask line.
		for _, m := range regexp.MustCompile(`trs_[A-Za-z0-9_-]{40,}`).FindAllString(out, -1) {
			if !regexp.MustCompile(`::add-mask::` + regexp.QuoteMeta(m)).MatchString(out) {
				t.Fatalf("publish in GitHub Actions printed a session token without masking it:\n%s", out)
			}
		}

		for _, v := range []string{"1.0", "2.0"} {
			p := fmt.Sprintf("com/acme/ci/%[1]s/ci-%[1]s.txt", v)
			var f struct {
				Build *struct {
					Provider   string `json:"provider"`
					Repository string `json:"repository"`
					Revision   string `json:"revision"`
				} `json:"build"`
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+p, nil, s.AdminBasic(), &f)
			if f.Build == nil || f.Build.Provider != "github-actions" || f.Build.Repository != kcCIRepository || f.Build.Revision != kcCISHA {
				t.Fatalf("file %s: build %+v, want the build record of the CI token", p, f.Build)
			}
		}
		n := 0
		for _, it := range publishLog(t, s, repo) {
			if it.Via == "oidc" {
				n++
			}
		}
		if n != 2 {
			t.Fatalf("publish log of %s: %+v, want two CI sessions", repo, publishLog(t, s, repo))
		}
	})
}

// TestCLIUpload: publish FILE uploads files into the folder "path" of a raw repo (a file of
// 64 MiB or more through the resumable upload routes) and .deb packages to the distribution and
// component of a Debian repo, which apt then installs.
func TestCLIUpload(t *testing.T) {
	c := newCLI(t)
	debs := buildDebPackages(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		raw, deb := RandName("files"), RandName("apt")
		s.CreateRepo(t, raw, "raw", false, nil)
		s.CreateRepo(t, deb, "debian", true, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		for _, r := range []string{raw, deb} {
			s.SetPerms(t, r, dev, "repo.download", "repo.publish")
		}
		out := c.mustRun(t, debImage, "publish files", `set -e
mkdir -p /work/files
cd /work/files
echo '=== raw setup ==='
telesphorepo setup "$RAW_LINK"
printf 'path = "dist"\n' >> telesphorepo.toml
printf 'small file\n' > small.txt
head -c 68157440 /dev/urandom > big.bin
echo "big sha256: $(sha256sum big.bin | cut -d ' ' -f 1)"
echo '=== raw publish ==='
telesphorepo publish small.txt big.bin
rm big.bin
mkdir -p /work/deb
cd /work/deb
echo '=== deb setup ==='
telesphorepo setup "$DEB_LINK"
sed -i 's/^dist = .*/dist = "trixie"/' telesphorepo.toml
cat telesphorepo.toml
echo '=== deb publish ==='
telesphorepo publish /work/pkgs/hello-1.0.deb /work/pkgs/native.deb
`, ClientOpts{
			Files: []ContainerFile{{Path: "/work/pkgs/hello-1.0.deb", Content: string(debs.hello1)}, {Path: "/work/pkgs/native.deb", Content: string(debs.native)}},
			Env:   map[string]string{"RAW_LINK": s.NewSetupCode(t, dev, raw).Link, "DEB_LINK": s.NewSetupCode(t, dev, deb).Link},
		})

		base := s.InternalURL + "/raw/" + raw + "/dist/"
		wantText(t, "publish raw files", section(out, "raw publish"), "Uploaded small.txt to "+base+"small.txt",
			"Uploading big.bin (68157440 bytes) in chunks of 16 MiB", "Uploaded big.bin to "+base+"big.bin")
		m := regexp.MustCompile(`big sha256: ([0-9a-f]{64})`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no SHA-256 of big.bin in the output:\n%s", out)
		}
		resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+raw+"/dist/small.txt", nil), s.AdminBasic())
		if resp.StatusCode != http.StatusOK || string(b) != "small file\n" {
			t.Fatalf("GET small.txt: status %d: %q", resp.StatusCode, b)
		}
		resp, b = s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+raw+"/dist/big.bin", nil), s.AdminBasic())
		if sum := sha256.Sum256(b); resp.StatusCode != http.StatusOK || hex.EncodeToString(sum[:]) != m[1] {
			t.Fatalf("GET big.bin: status %d, %d bytes, want the file of SHA-256 %s", resp.StatusCode, len(b), m[1])
		}

		wantText(t, "set up a Debian repo", section(out, "deb setup"), "Packages go to distribution stable, component main.", `dist = "trixie"`)
		wantText(t, "publish .deb packages", section(out, "deb publish"), "Uploaded e2e-hello 1.0 (all) to trixie/main of "+deb,
			"Uploaded e2e-native 1.0 ("+debs.arch+") to trixie/main of "+deb)
		s.WaitDebianIndexed(t, deb)
		apt := debAptInstall(t, s, deb, s.AdminUser, s.AdminPass, s.DebianKey(t, deb, nil))
		wantText(t, "apt install", apt, "RESULT: hello 1.0", "NATIVE: native "+debs.arch)
	})
}
