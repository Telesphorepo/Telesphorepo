//go:build e2e || import || scale || clients

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	osexec "os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The images of the tools that people bring (Maven, Gradle, Docker, Helm, Jenkins, Keycloak,
// Nexus, ...) float: each names the line that people follow (latest, stable, lts), and every run
// pulls it again, so the tests meet each new release of a tool. The infrastructure of the tests
// (SeaweedFS, Caddy, the OCI conformance suite) stays pinned. A run prints the version of each
// floating image that it pulled.

var (
	floatingMu   sync.Mutex
	floatingRefs []string
)

// floatingImage registers ref as a floating image of this zone and returns it. The files of a
// zone register their images in package variables, so a run pulls only the images of its zone.
func floatingImage(ref string) string {
	floatingMu.Lock()
	defer floatingMu.Unlock()
	floatingRefs = append(floatingRefs, ref)
	return ref
}

// pullFloating pulls every floating image of this zone, at the same time, and prints the version
// of each.
func pullFloating(ctx context.Context) error {
	if v, err := newestRelease(ctx, "oras-project/oras"); err != nil {
		return fmt.Errorf("the newest ORAS release: %w", err)
	} else {
		orasImage = "ghcr.io/oras-project/oras:" + v
		floatingImage(orasImage)
	}
	floatingMu.Lock()
	refs := append([]string(nil), floatingRefs...)
	floatingMu.Unlock()
	errs := make([]error, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
			defer cancel()
			if out, err := osexec.CommandContext(ctx, Engine.CLI, "pull", "-q", ref).CombinedOutput(); err != nil {
				errs[i] = fmt.Errorf("pull %s: %w\n%s", ref, err, out)
			}
		})
	}
	wg.Wait()
	for i, ref := range refs {
		if errs[i] != nil {
			return errs[i]
		}
		out, _ := osexec.Command(Engine.CLI, "image", "inspect", "--format",
			`{{index .Config.Labels "org.opencontainers.image.version"}} {{.Created}}`, ref).Output()
		fmt.Fprintf(os.Stderr, "e2e: image %s: %s\n", ref, strings.TrimSpace(string(out)))
	}
	return nil
}

var stableTagRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// newestRelease returns the newest tag vX.Y.Z (no pre-release) of the GitHub Container Registry
// repository repo.
func newestRelease(ctx context.Context, repo string) (string, error) {
	get := func(url, token string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := get("https://ghcr.io/token?scope=repository:"+repo+":pull", "", &tok); err != nil {
		return "", err
	}
	var list struct {
		Tags []string `json:"tags"`
	}
	if err := get("https://ghcr.io/v2/"+repo+"/tags/list?n=10000", tok.Token, &list); err != nil {
		return "", err
	}
	best, bestKey := "", [3]int{-1}
	for _, tag := range list.Tags {
		m := stableTagRE.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		var k [3]int
		for i := range k {
			k[i], _ = strconv.Atoi(m[i+1])
		}
		if k[0] > bestKey[0] || k[0] == bestKey[0] && (k[1] > bestKey[1] || k[1] == bestKey[1] && k[2] > bestKey[2]) {
			best, bestKey = tag, k
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s has no release tag", repo)
	}
	return best, nil
}
