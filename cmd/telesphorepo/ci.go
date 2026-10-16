package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	return strings.TrimSpace(os.Getenv("OIDC_TOKEN")), nil
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
