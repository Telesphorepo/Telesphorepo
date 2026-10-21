//go:build e2e

package e2e

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// plainClient sends no Accept-Encoding of its own: the Go transport adds gzip and decodes the
// answer unless compression is off, so each request sets Accept-Encoding itself.
var plainClient = &http.Client{
	Timeout:   2 * time.Minute,
	Transport: &http.Transport{DisableCompression: true},
}

// getEncoded sends GET url with the Accept-Encoding acceptEncoding (none when empty) and returns
// the answer and its body as sent.
func getEncoded(t *testing.T, url, acceptEncoding string, header ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := plainClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("GET %s (Accept-Encoding %q): status %d: %s", url, acceptEncoding, resp.StatusCode, b)
	}
	return resp, b
}

func gunzip(t *testing.T, what string, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: not gzip: %v", what, err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("%s: gunzip: %v", what, err)
	}
	return out
}

func varies(resp *http.Response) bool {
	for _, v := range resp.Header.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), "Accept-Encoding") {
				return true
			}
		}
	}
	return false
}

var assetScript = regexp.MustCompile(`/assets/[^"']+\.js`)

// TestCompressUI fetches index.html and its main script plain, gzip and brotli: each encoded
// answer carries Content-Encoding, Vary: Accept-Encoding, the Content-Type and Cache-Control of
// the plain file, and decodes to the plain bytes. A Range request gets the plain bytes.
func TestCompressUI(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)

		_, index := getEncoded(t, s.URL+"/", "")
		script := assetScript.Find(index)
		if script == nil {
			t.Fatalf("index.html names no /assets/ script:\n%s", index)
		}
		for _, path := range []string{"/", "/repo/x/files/", string(script)} {
			plainResp, plain := getEncoded(t, s.URL+path, "")
			if ce := plainResp.Header.Get("Content-Encoding"); ce != "" {
				t.Fatalf("GET %s without Accept-Encoding: Content-Encoding %q", path, ce)
			}
			if !varies(plainResp) {
				t.Fatalf("GET %s without Accept-Encoding: Vary %q lacks Accept-Encoding", path, plainResp.Header.Values("Vary"))
			}

			resp, gz := getEncoded(t, s.URL+path, "gzip")
			if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
				t.Fatalf("GET %s with gzip: Content-Encoding %q", path, ce)
			}
			for _, h := range []string{"Content-Type", "Cache-Control"} {
				if resp.Header.Get(h) != plainResp.Header.Get(h) {
					t.Fatalf("GET %s with gzip: %s %q, plain %q", path, h, resp.Header.Get(h), plainResp.Header.Get(h))
				}
			}
			if !varies(resp) {
				t.Fatalf("GET %s with gzip: Vary %q lacks Accept-Encoding", path, resp.Header.Values("Vary"))
			}
			if got := gunzip(t, "GET "+path, gz); !bytes.Equal(got, plain) {
				t.Fatalf("GET %s with gzip decodes to %d bytes, plain is %d", path, len(got), len(plain))
			}

			resp, br := getEncoded(t, s.URL+path, "br, gzip")
			if ce := resp.Header.Get("Content-Encoding"); ce != "br" {
				t.Fatalf("GET %s with br, gzip: Content-Encoding %q", path, ce)
			}
			if resp.Header.Get("Content-Type") != plainResp.Header.Get("Content-Type") {
				t.Fatalf("GET %s with br: Content-Type %q, plain %q", path, resp.Header.Get("Content-Type"), plainResp.Header.Get("Content-Type"))
			}
			if len(br) >= len(plain) {
				t.Fatalf("GET %s with br: %d bytes, plain %d", path, len(br), len(plain))
			}

			resp, part := getEncoded(t, s.URL+path, "br, gzip", "Range", "bytes=0-9")
			if ce := resp.Header.Get("Content-Encoding"); ce != "" || !bytes.Equal(part, plain[:min(10, len(plain))]) {
				t.Fatalf("GET %s with Range: Content-Encoding %q, body %q", path, ce, part)
			}
		}

		// The Go stdlib has no brotli decoder: curl decodes the brotli answer.
		_, plain := getEncoded(t, s.URL+string(script), "")
		sum := sha256.Sum256(plain)
		code, out := RunClient(t, volumeToolImage, ClientOpts{Cmd: []string{
			"curl -sS --fail --compressed -H 'Accept-Encoding: br' -D /tmp/h -o /tmp/b " + s.InternalURL + string(script) +
				" && grep -i '^content-encoding:' /tmp/h && sha256sum /tmp/b",
		}})
		if code != 0 {
			t.Fatalf("curl brotli: exit %d:\n%s", code, out)
		}
		if !strings.Contains(strings.ToLower(out), "content-encoding: br") || !strings.Contains(out, hex.EncodeToString(sum[:])) {
			t.Fatalf("curl brotli of %s: want Content-Encoding br and sha256 %x:\n%s", script, sum, out)
		}
	})
}

// TestCompressAPI reads GET /api/formats (several KiB of JSON) with and without gzip, and a small
// answer (GET /api/me) that stays plain.
func TestCompressAPI(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)

		plainResp, plain := getEncoded(t, s.URL+"/api/formats", "")
		if ce := plainResp.Header.Get("Content-Encoding"); ce != "" {
			t.Fatalf("GET /api/formats without Accept-Encoding: Content-Encoding %q", ce)
		}
		if !json.Valid(plain) || len(plain) < 1024 {
			t.Fatalf("GET /api/formats: %d bytes, want valid JSON of 1 KiB or more:\n%s", len(plain), plain)
		}
		if !varies(plainResp) {
			t.Fatalf("GET /api/formats: Vary %q lacks Accept-Encoding", plainResp.Header.Values("Vary"))
		}

		resp, gz := getEncoded(t, s.URL+"/api/formats", "br;q=1, gzip;q=0.8")
		if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
			t.Fatalf("GET /api/formats with gzip: Content-Encoding %q", ce)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("GET /api/formats with gzip: Content-Type %q", ct)
		}
		if !varies(resp) {
			t.Fatalf("GET /api/formats with gzip: Vary %q lacks Accept-Encoding", resp.Header.Values("Vary"))
		}
		if got := gunzip(t, "GET /api/formats", gz); !json.Valid(got) || !bytes.Equal(got, plain) {
			t.Fatalf("GET /api/formats with gzip decodes to %d bytes (valid JSON %v), plain is %d", len(got), json.Valid(got), len(plain))
		}

		resp, _ = getEncoded(t, s.URL+"/api/formats", "gzip;q=0")
		if ce := resp.Header.Get("Content-Encoding"); ce != "" {
			t.Fatalf("GET /api/formats with gzip;q=0: Content-Encoding %q", ce)
		}

		// A small answer stays plain: the refusal of an anonymous /api/me.
		req, _ := http.NewRequest(http.MethodGet, s.URL+"/api/me", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		r, err := plainClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if ce := r.Header.Get("Content-Encoding"); ce != "" || !json.Valid(b) {
			t.Fatalf("GET /api/me with gzip: status %d, Content-Encoding %q, body %q", r.StatusCode, ce, b)
		}
	})
}
