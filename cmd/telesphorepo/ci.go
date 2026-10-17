package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// inCI reports whether the CLI runs in a CI job: CI, GITHUB_ACTIONS, JENKINS_URL or JENKINS_HOME
// is set.
func inCI() bool {
	for _, k := range []string{"CI", "GITHUB_ACTIONS", "JENKINS_URL", "JENKINS_HOME"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// inGitHubActions reports whether the CLI runs in a GitHub Actions job.
func inGitHubActions() bool { return os.Getenv("GITHUB_ACTIONS") == "true" }

// mask tells GitHub Actions to hide secret in the log of the job ("::add-mask::"). It writes to
// w, which the runner reads.
func mask(w io.Writer, secret string) {
	if inGitHubActions() && secret != "" {
		fmt.Fprintln(w, "::add-mask::"+secret)
	}
}

// ciToken returns the CI identity token of the job, in this order: GitHub Actions
// (ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN, with the audience aud, the
// canonical server URL), then OIDC_TOKEN (the Jenkins binding of every Jenkinsfile, and any CI
// that exports an identity token under that name). It returns "" when the job has none.
func ciToken(ctx context.Context, aud string) (string, error) {
	if u, t := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"); u != "" && t != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+sep+"audience="+url.QueryEscape(aud), nil)
		if err != nil {
			return "", fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL is not a valid URL: %w", unwrapURL(err))
		}
		req.Header.Set("Authorization", "bearer "+t)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("cannot get the identity token of the GitHub Actions job: %w", unwrapURL(err))
		}
		defer resp.Body.Close()
		var out struct {
			Value string `json:"value"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out.Value == "" {
			return "", fmt.Errorf("GitHub Actions answered %d to the request for an identity token; the job needs permissions: id-token: write", resp.StatusCode)
		}
		return out.Value, nil
	}
	tok := strings.TrimSpace(os.Getenv("OIDC_TOKEN"))
	if tok == "" {
		return "", nil
	}
	if err := checkAudience(tok, aud); err != nil {
		return "", err
	}
	return tok, nil
}

// checkAudience refuses the identity token tok unless its audience (the "aud" claim) holds the
// server URL aud, with or without a trailing slash. The CI fixes the audience of OIDC_TOKEN, and
// the server of telesphorepo.toml may come from a branch that nobody checked. So the CLI never
// sends the token to a server that the token is not for. The CLI reads the claim without a check
// of the signature; the server checks the signature.
func checkAudience(tok, aud string) error {
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return errors.New("OIDC_TOKEN is not a JWT identity token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return errors.New("OIDC_TOKEN is not a JWT identity token")
	}
	var auds []string
	var one string
	if json.Unmarshal(claims.Aud, &one) == nil {
		auds = []string{one}
	} else if json.Unmarshal(claims.Aud, &auds) != nil {
		auds = nil
	}
	for _, a := range auds {
		if a == aud || a == aud+"/" {
			return nil
		}
	}
	return fmt.Errorf("the identity token in OIDC_TOKEN is for %s, not for %s. The CLI sends it only to the server that it is for: "+
		"set the audience of the identity token credential to %s, or correct the server URL",
		orNone(strings.Join(auds, ", ")), aud, aud)
}

func orNone(s string) string {
	if s == "" {
		return "no server"
	}
	return s
}

var revisionRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ciRevision returns the commit that the CI job builds, for the build record of the server: the
// Jenkins GIT_COMMIT, then GITHUB_SHA, then "git rev-parse HEAD" in dir. It returns "" when none
// is a commit hash. GitHub Actions also names the commit in its signed token, and the server takes
// that one first.
func ciRevision(dir string) string {
	for _, k := range []string{"GIT_COMMIT", "GITHUB_SHA"} {
		if v := strings.ToLower(strings.TrimSpace(os.Getenv(k))); revisionRE.MatchString(v) {
			return v
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if v := strings.TrimSpace(string(out)); err == nil && revisionRE.MatchString(v) {
		return v
	}
	return ""
}

// childEnv is the environment of a child process: the CLI's own without OIDC_TOKEN, so a build
// step cannot mint for other repos with it, and with extra after it.
func childEnv(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "OIDC_TOKEN") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}
