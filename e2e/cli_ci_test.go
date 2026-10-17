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

// jobCommit is the GIT_COMMIT of the CI jobs of the CLI tests.
const jobCommit = "fedcba9876543210fedcba9876543210fedcba98"

// TestCLICI: in CI, publish mints the session with the identity token of the job, from the
// GitHub Actions token endpoint (a jwebserver that serves a Keycloak token) or from OIDC_TOKEN
// (Jenkins); the files get the build record of the token, with the commit of the token, else the
// GIT_COMMIT of the job; the child process never sees OIDC_TOKEN; a GitHub Actions job without
// id-token: write gets the message.
func TestCLICI(t *testing.T) {
	c := newCLI(t)
	kc := newKeycloak(t)
	alias := ciAlias(t, kc)
	gh := startGitHubTokenServer(t)
	j := newJenkinsIssuer(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		opts.Alias = alias
		s := StartServer(t, opts)
		repo := RandName("ci")
		s.CreateRepo(t, repo, "maven", false, nil)
		createCITrust(t, s, kc.Issuer, repo)
		createJenkinsTrust(t, s, j, "team/app", repo)
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

		// The token names its commit, so the GIT_COMMIT of the job does not count.
		tok := kc.CIToken(t)
		out = c.mustRun(t, curlImage, "Jenkins", "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: map[string]string{
			"JENKINS_URL": "http://jenkins.example/", "OIDC_TOKEN": tok, "E2E_VERSION": "1.0", "E2E_SECRET": tok, "GIT_COMMIT": jobCommit}})
		wantText(t, "publish in Jenkins", out, "Publishing to "+repo+" (maven)", "child: OIDC_TOKEN=unset", "child: TELESPHOREPO_TOKEN set")

		// A token of the Jenkins plugin names no commit: the build record takes GIT_COMMIT.
		jt := j.Token(t, s.InternalURL, "team/app", 12)
		c.mustRun(t, curlImage, "Jenkins plugin token", "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: map[string]string{
			"JENKINS_URL": "http://jenkins.example/", "OIDC_TOKEN": jt, "E2E_VERSION": "3.0", "E2E_SECRET": jt, "GIT_COMMIT": jobCommit}})
		if b := buildOf(t, s, repo, "com/acme/ci/3.0/ci-3.0.txt"); b == nil || b.Provider != "jenkins" || b.Run != "12" || b.Revision != jobCommit {
			t.Fatalf("build of the Jenkins publish: %+v, want Jenkins team/app #12 at %s", b, jobCommit)
		}

		// A project file that names another server gets no CI token: its audience is this server.
		other := []ContainerFile{{Path: "/work/app/telesphorepo.toml", Content: "server = \"http://other.invalid:8080\"\nrepo = \"" + repo + "\"\n"},
			files[1], files[2]}
		code, out = c.run(t, curlImage, "Jenkins with another server", "cd /work/app && telesphorepo publish", ClientOpts{Files: other, Env: map[string]string{
			"JENKINS_URL": "http://jenkins.example/", "OIDC_TOKEN": tok, "E2E_SECRET": tok}})
		if code != 1 {
			t.Fatalf("publish in Jenkins to another server: exit %d, want 1:\n%s", code, out)
		}
		wantText(t, "publish in Jenkins to another server", out, "the identity token in OIDC_TOKEN is for "+s.InternalURL+", not for http://other.invalid:8080")

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
		if n != 3 {
			t.Fatalf("publish log of %s: %+v, want three CI sessions", repo, publishLog(t, s, repo))
		}
	})
}

// TestCLIUpload: publish FILE uploads files into the folder "path" of a raw repo (a file of
// 64 MiB or more through the resumable upload routes) and .deb packages to the distribution and
// component of a Debian repo (a package of 64 MiB or more through the resumable upload routes as
// well), which apt then installs.
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
mkdir -p /tmp/big/DEBIAN /tmp/big/usr/share/e2e-big
printf 'Package: e2e-big\nVersion: 1.0\nArchitecture: all\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: big\n' > /tmp/big/DEBIAN/control
head -c 68157440 /dev/urandom > /tmp/big/usr/share/e2e-big/blob
dpkg-deb --root-owner-group -Zgzip --build /tmp/big /tmp/e2e-big.deb >/dev/null
rm -rf /tmp/big
echo '=== big deb publish ==='
telesphorepo publish /tmp/e2e-big.deb
rm /tmp/e2e-big.deb
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
		if b := getFollowS3(t, s, opts.S3, "/raw/"+raw+"/dist/small.txt"); string(b) != "small file\n" {
			t.Fatalf("GET small.txt: %q", b)
		}
		b := getFollowS3(t, s, opts.S3, "/raw/"+raw+"/dist/big.bin")
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != m[1] {
			t.Fatalf("GET big.bin: %d bytes, want the file of SHA-256 %s", len(b), m[1])
		}

		wantText(t, "set up a Debian repo", section(out, "deb setup"), "Packages go to distribution stable, component main.", `dist = "trixie"`)
		wantText(t, "publish .deb packages", section(out, "deb publish"), "Uploaded e2e-hello 1.0 (all) to trixie/main of "+deb,
			"Uploaded e2e-native 1.0 ("+debs.arch+") to trixie/main of "+deb)
		wantText(t, "publish a large .deb package", section(out, "big deb publish"), "Uploading /tmp/e2e-big.deb (", "in chunks of 16 MiB",
			"Uploaded e2e-big 1.0 (all) to trixie/main of "+deb)
		s.WaitDebianIndexed(t, deb)
		apt := debAptInstall(t, s, deb, s.AdminUser, s.AdminPass, s.DebianKey(t, deb, nil))
		wantText(t, "apt install", apt, "RESULT: hello 1.0", "NATIVE: native "+debs.arch)
	})
}
