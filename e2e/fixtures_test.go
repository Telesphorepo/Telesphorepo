//go:build e2e || import

package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Fixtures that the core zone and the import zone share: an OCI image pushed over the registry
// API and a .nupkg.

// ociErrorCode returns the code of the first error of an OCI error body, or "".
func ociErrorCode(b []byte) string {
	var e struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &e) != nil || len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Code
}

// wantOCI fails the test unless the answer has the status and the OCI error code.
func wantOCI(t *testing.T, what string, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	if resp.StatusCode != status || (code != "" && ociErrorCode(body) != code) {
		t.Fatalf("%s: status %d %q, want %d %s: %s", what, resp.StatusCode, ociErrorCode(body), status, code, body)
	}
}

// ociDo sends method to the /v2/ path of s with auth, body and headers.
func ociDo(t *testing.T, s *Server, method, path string, body []byte, auth Auth, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := s.NewRequest(t, method, path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return s.Do(t, req, auth)
}

// ociBlob uploads b to name in one request (POST with digest=) and returns its hex.
func ociBlob(t *testing.T, s *Server, auth Auth, name string, b []byte) string {
	t.Helper()
	h := sha256Hex(b)
	resp, body := ociDo(t, s, http.MethodPost, "/v2/"+name+"/blobs/uploads/?digest=sha256:"+h, b, auth, nil)
	wantOCI(t, "upload a blob to "+name, resp, body, http.StatusCreated, "")
	return h
}

// ociImage pushes an OCI image manifest of a fresh config and the layers to name:ref (a tag or
// a digest) and returns the manifest's hex, its bytes and the hex of its config.
func ociImage(t *testing.T, s *Server, auth Auth, name, ref string, extra map[string]any, layers ...[]byte) (string, []byte, string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"created":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","x":"` + randHex(8) + `"}`)
	ch := ociBlob(t, s, auth, name, config)
	var ls []map[string]any
	for _, l := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + ociBlob(t, s, auth, name, l), "size": len(l)})
	}
	m := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + ch, "size": len(config)},
		"layers": ls}
	for k, v := range extra {
		m[k] = v
	}
	if ls == nil {
		m["layers"] = []any{}
	}
	mb, _ := json.Marshal(m)
	resp, body := ociDo(t, s, http.MethodPut, "/v2/"+name+"/manifests/"+ref, mb, auth, map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
	wantOCI(t, "put the manifest "+name+":"+ref, resp, body, http.StatusCreated, "")
	mh := sha256Hex(mb)
	if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+mh {
		t.Fatalf("put the manifest %s:%s: Docker-Content-Digest %q, want sha256:%s", name, ref, got, mh)
	}
	return mh, mb, ch
}

// testNupkg is a .nupkg of package id at version: a zip with the .nuspec at its root and a file.
func testNupkg(t *testing.T, id, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		id + ".nuspec": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>` + id + `</id>
    <version>` + version + `</version>
    <authors>e2e</authors>
    <description>A package of the e2e tests.</description>
  </metadata>
</package>
`,
		"content/data.txt": strings.Repeat("e2e nuget chunk\n", 4096),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
