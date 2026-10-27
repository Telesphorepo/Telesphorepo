//go:build e2e || import || clients

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

	"github.com/testcontainers/testcontainers-go"
)

// The images of the tools that people bring (Maven, Gradle, Docker, Helm, Jenkins, Keycloak,
// Nexus, ...) float: each names the line that people follow (latest, stable, lts), and every run
// pulls it again, so the tests meet each new release of a tool. The infrastructure of the tests
// (SeaweedFS, Caddy, the OCI conformance suite) stays pinned. A run prints the version of each
// floating image that it pulled.

var (
	floatingMu    sync.Mutex
	floatingRefs  []string
	floatingAmd64 = map[string]bool{}
)

// floatingAmd64Image registers ref as a floating image that runs as linux/amd64 on every host
// (with runAmd64): Arch Linux has no arm64 build, and the dnf clients install the x86_64 test
// packages. An arm64 host runs it emulated.
func floatingAmd64Image(ref string) string {
	floatingMu.Lock()
	floatingAmd64[ref] = true
	floatingMu.Unlock()
	return floatingImage(ref)
}

// floatingImage registers ref as a floating image of this zone and returns it. The files of a
// zone register their images in package variables, so a run pulls only the images of its zone.
func floatingImage(ref string) string {
	floatingMu.Lock()
	defer floatingMu.Unlock()
	floatingRefs = append(floatingRefs, ref)
	return ref
}

// runAmd64 runs a container of a floatingAmd64Image as linux/amd64.
var runAmd64 = testcontainers.CustomizeRequestOption(func(r *testcontainers.GenericContainerRequest) error {
	r.ImagePlatform = "linux/amd64"
	return nil
})

// pullFloating pulls every floating image of this zone, four at a time, and prints the version
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
	// Four pulls at once: each pull of Podman on Windows and macOS opens an SSH connection to its
	// machine, which refuses a burst of them.
	slots := make(chan struct{}, 4)
	for i, ref := range refs {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
			defer cancel()
			if hubCurrent(ctx, ref) {
				return
			}
			args := []string{"pull", "-q", ref}
			if floatingAmd64[ref] {
				args = []string{"pull", "-q", "--platform", "linux/amd64", ref}
			}
			if out, err := osexec.CommandContext(ctx, Engine.CLI, args...).CombinedOutput(); err != nil {
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

// hubCurrent reports whether ref is a Docker Hub image whose local copy is the tag's current
// manifest. Docker Hub counts every manifest GET against its anonymous limit (100 an hour), and a
// pull of an image that did not change still makes one, so a few runs in an hour used the limit
// up. A HEAD of the manifest is not counted: when its digest is one of the local image's repo
// digests, the pull is skipped. Any other registry, or any error, answers false and the image is
// pulled.
func hubCurrent(ctx context.Context, ref string) bool {
	repo, tag, ok := hubRef(ref)
	if !ok {
		return false
	}
	out, err := osexec.CommandContext(ctx, Engine.CLI, "image", "inspect", "--format",
		`{{range .RepoDigests}}{{println .}}{{end}}`, ref).Output()
	if err != nil {
		return false // not here yet
	}
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://auth.docker.io/token?service=registry.docker.io&scope=repository:"+repo+":pull", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(tokReq)
	if err != nil {
		return false
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://registry-1.docker.io/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	head, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	head.Body.Close()
	digest := head.Header.Get("Docker-Content-Digest")
	if head.StatusCode != http.StatusOK || digest == "" {
		return false
	}
	for line := range strings.Lines(string(out)) {
		if strings.HasSuffix(strings.TrimSpace(line), "@"+digest) {
			return true
		}
	}
	return false
}

// hubRef splits a Docker Hub image reference into its repository (library/ for an official
// image) and tag. ok is false for another registry or a reference by digest.
func hubRef(ref string) (repo, tag string, ok bool) {
	if strings.Contains(ref, "@") {
		return "", "", false
	}
	name := ref
	if first, rest, found := strings.Cut(ref, "/"); found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if first != "docker.io" {
			return "", "", false
		}
		name = rest
	}
	tag = "latest"
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name, tag = name[:i], name[i+1:]
	}
	if !strings.Contains(name, "/") {
		name = "library/" + name
	}
	return name, tag, true
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
