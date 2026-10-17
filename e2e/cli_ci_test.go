//go:build clients

package e2e

import (
	"net/http"
	"os"
	"regexp"
	"testing"
)

// gitHubIssuer is the issuer of the identity tokens of GitHub Actions jobs.
const gitHubIssuer = "https://token.actions.githubusercontent.com"

// TestCLIGitHubActions runs in the e2e-github-actions job of ci.yml, which has
// "permissions: id-token: write": the CLI in a Maven container gets the env of the job, asks
// GitHub for an identity token with the server as the audience, and publishes with mvn deploy.
// The server checks the token with the keys of GitHub. The file carries the build record of the
// job: the repository, the run and the commit of the signed token, also when GIT_COMMIT names
// another one. The CLI prints a mask line for each token. A job without id-token: write gets the
// message that names it. Outside GitHub Actions the test has no token and skips; with
// E2E_GITHUB_ACTIONS set (the job of ci.yml) it fails without one.
func TestCLIGitHubActions(t *testing.T) {
	requestURL, requestToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if requestURL == "" || requestToken == "" {
		if os.Getenv("E2E_GITHUB_ACTIONS") != "" {
			t.Fatal("the job has no identity token: it needs permissions: id-token: write")
		}
		t.Skip("no GitHub Actions identity token: this test runs in the e2e-github-actions job of ci.yml")
	}
	repository, sha := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_SHA")
	c := newCLI(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("ci")
		s.CreateRepo(t, repo, "maven", false, nil)
		s.MustAPI(t, http.MethodPost, "/api/publish/trusts", map[string]any{
			"name": "github", "issuer": gitHubIssuer, "audience": "", "jwks": "",
			"claims": map[string][]string{"repository": {repository}},
			"repos":  []string{repo},
		}, s.Admin(), nil)
		files := []ContainerFile{projectFile(s, "/work/app", repo), {Path: "/work/app/pom.xml", Content: ciPOM}}
		job := map[string]string{"CI": "true", "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": repository, "GITHUB_SHA": sha}

		code, out := c.run(t, mavenImage, "a job without id-token: write", "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: job})
		if code != 1 {
			t.Fatalf("publish in a job without an identity token: exit %d, want 1:\n%s", code, out)
		}
		wantText(t, "publish in a job without an identity token", out, "permissions: id-token: write")

		env := map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": requestURL, "ACTIONS_ID_TOKEN_REQUEST_TOKEN": requestToken,
			"GIT_COMMIT": "fedcba9876543210fedcba9876543210fedcba98"}
		for k, v := range job {
			env[k] = v
		}
		out = c.mustRun(t, mavenImage, "publish in GitHub Actions", "cd /work/app && telesphorepo publish", ClientOpts{Files: files, Env: env,
			Mounts: map[string]string{M2Volume: "/root/.m2/repository"}})
		wantText(t, "publish in GitHub Actions", out, "Publishing to "+repo+" (maven)", "BUILD SUCCESS", "::add-mask::", oidcUnset)
		// The CLI masks each session token before Maven starts: no token appears without its mask line.
		for _, m := range regexp.MustCompile(`trs_[A-Za-z0-9_-]{40,}`).FindAllString(out, -1) {
			if !regexp.MustCompile(`::add-mask::` + regexp.QuoteMeta(m)).MatchString(out) {
				t.Fatalf("publish in GitHub Actions printed a session token without masking it:\n%s", out)
			}
		}
		if b := buildOf(t, s, repo, ciJar); b == nil || b.Provider != "github-actions" || b.Repository != repository || b.Revision != sha || b.Run != os.Getenv("GITHUB_RUN_ID") {
			t.Fatalf("build of %s: %+v, want GitHub Actions %s run %s at %s", ciJar, b, repository, os.Getenv("GITHUB_RUN_ID"), sha)
		}
	})
}
