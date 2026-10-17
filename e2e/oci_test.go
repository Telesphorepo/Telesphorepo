//go:build e2e || import

package e2e

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

// OCI images that the registry tests of several zones share: real tar layers, OCI image layouts
// for skopeo, and a push over the registry API to any registry.

var (
	craneImage  = floatingImage("gcr.io/go-containerregistry/crane:debug") // debug: the newest release with a shell
	skopeoImage = floatingImage("quay.io/skopeo/stable:latest")
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// tarFile writes a tar archive with one file name of content to w.
func tarFile(t *testing.T, w io.Writer, name string, content []byte) {
	t.Helper()
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

// ociImageBlobs is an OCI image of uncompressed tar layers: its blobs by hex and its manifest.
type ociImageBlobs struct {
	Blobs    map[string][]byte // config and layers
	Order    []string          // the hex of the config, then of each layer
	Manifest []byte
	Hex      string // the hex of the manifest
	Layers   []string
}

// tarImage builds an image for linux/arch with one layer per file (a tar of a file named
// f<i>), so Docker unpacks it.
func tarImage(t *testing.T, arch string, files ...[]byte) ociImageBlobs {
	t.Helper()
	img := ociImageBlobs{Blobs: map[string][]byte{}}
	var layers []map[string]any
	var diffIDs []string
	for i, content := range files {
		var buf bytes.Buffer
		tarFile(t, &buf, fmt.Sprintf("f%d", i), content)
		l := buf.Bytes()
		h := sha256Hex(l)
		img.Blobs[h] = l
		img.Layers = append(img.Layers, h)
		diffIDs = append(diffIDs, "sha256:"+h)
		layers = append(layers, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + h, "size": len(l)})
	}
	config, _ := json.Marshal(map[string]any{"architecture": arch, "os": "linux", "created": time.Now().UTC().Format(time.RFC3339Nano),
		"config": map[string]any{"Cmd": []string{"/f0"}, "Labels": map[string]string{"e2e": randHex(8)}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs}})
	ch := sha256Hex(config)
	img.Blobs[ch] = config
	img.Order = append([]string{ch}, img.Layers...)
	img.Manifest, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + ch, "size": len(config)},
		"layers": layers})
	img.Hex = sha256Hex(img.Manifest)
	return img
}

// hostArch is the architecture of the images that a Docker engine on this host runs.
func hostArch() string { return runtime.GOARCH }

// ociLayout returns the files of an OCI image layout at dir with one image (tag v1) of one
// uncompressed layer with content, and the hex of its manifest.
func ociLayout(t *testing.T, dir string, content []byte) ([]ContainerFile, string) {
	t.Helper()
	img := tarImage(t, "amd64", content)
	index := fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:%s","size":%d,"annotations":{"org.opencontainers.image.ref.name":"v1"}}]}`,
		img.Hex, len(img.Manifest))
	files := []ContainerFile{
		{Path: dir + "/oci-layout", Content: `{"imageLayoutVersion":"1.0.0"}`},
		{Path: dir + "/index.json", Content: index},
		{Path: dir + "/blobs/sha256/" + img.Hex, Content: string(img.Manifest)},
	}
	for h, b := range img.Blobs {
		files = append(files, ContainerFile{Path: dir + "/blobs/sha256/" + h, Content: string(b)})
	}
	return files, img.Hex
}

// ociIndexLayout returns the files of an OCI image layout at dir whose tag ref names an OCI index
// of two images (linux/amd64 and linux/arm64), and the hex of the index and of each image.
func ociIndexLayout(t *testing.T, dir, ref string) ([]ContainerFile, string, []string) {
	t.Helper()
	files := []ContainerFile{{Path: dir + "/oci-layout", Content: `{"imageLayoutVersion":"1.0.0"}`}}
	var children []map[string]any
	var hexes []string
	for _, arch := range []string{"amd64", "arm64"} {
		img := tarImage(t, arch, rawRandom(t, 64<<10))
		for h, b := range img.Blobs {
			files = append(files, ContainerFile{Path: dir + "/blobs/sha256/" + h, Content: string(b)})
		}
		files = append(files, ContainerFile{Path: dir + "/blobs/sha256/" + img.Hex, Content: string(img.Manifest)})
		children = append(children, map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + img.Hex,
			"size": len(img.Manifest), "platform": map[string]string{"os": "linux", "architecture": arch}})
		hexes = append(hexes, img.Hex)
	}
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
	ih := sha256Hex(idx)
	files = append(files, ContainerFile{Path: dir + "/blobs/sha256/" + ih, Content: string(idx)},
		ContainerFile{Path: dir + "/index.json", Content: fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:%s","size":%d,"annotations":{"org.opencontainers.image.ref.name":"%s"}}]}`,
			ih, len(idx), ref)})
	return files, ih, hexes
}

// registryClient sends OCI API requests to a registry at base (scheme and host) with a request
// decorator for the credentials.
type registryClient struct {
	base string
	auth func(r *http.Request)
}

// do sends one request and returns the response with its body read.
func (c registryClient) do(t *testing.T, method, rawURL string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	if strings.HasPrefix(rawURL, "/") {
		rawURL = c.base + rawURL
	}
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c.auth != nil {
		c.auth(req)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// pushBlob uploads b to name with POST and PUT, the way most clients do.
func (c registryClient) pushBlob(t *testing.T, name string, b []byte) {
	t.Helper()
	resp, body := c.do(t, http.MethodPost, "/v2/"+name+"/blobs/uploads/", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start an upload to %s: status %d: %s", name, resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("upload Location %q: %v", resp.Header.Get("Location"), err)
	}
	base, _ := url.Parse(c.base)
	u := base.ResolveReference(loc)
	q := u.Query()
	q.Set("digest", "sha256:"+sha256Hex(b))
	u.RawQuery = q.Encode()
	resp, body = c.do(t, http.MethodPut, u.String(), b, map[string]string{"Content-Type": "application/octet-stream"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("finish an upload to %s: status %d: %s", name, resp.StatusCode, body)
	}
}

// pushManifest puts a manifest of mediaType to name:ref.
func (c registryClient) pushManifest(t *testing.T, name, ref, mediaType string, m []byte) {
	t.Helper()
	resp, body := c.do(t, http.MethodPut, "/v2/"+name+"/manifests/"+ref, m, map[string]string{"Content-Type": mediaType})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put the manifest %s:%s: status %d: %s", name, ref, resp.StatusCode, body)
	}
}

// pushImage pushes img to name:tag.
func (c registryClient) pushImage(t *testing.T, name, tag string, img ociImageBlobs) {
	t.Helper()
	for _, h := range img.Order {
		c.pushBlob(t, name, img.Blobs[h])
	}
	c.pushManifest(t, name, tag, "application/vnd.oci.image.manifest.v1+json", img.Manifest)
}
