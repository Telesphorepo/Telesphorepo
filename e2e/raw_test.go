//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rawCurlScript uploads /work/app.bin with curl -T, downloads it again (following storage
// redirects) and compares the bytes.
const rawCurlScript = `set -e
echo "UPLOAD $(curl -sS -o /dev/null -w '%{http_code}' -u "$RAW_USER:$RAW_TOKEN" -T /work/app.bin "$RAW_URL")"
curl -fsSL -u "$RAW_USER:$RAW_TOKEN" -o /tmp/app.bin "$RAW_URL"
cmp /work/app.bin /tmp/app.bin
echo "RAW OK"
`

// rawGet GETs path and follows storage redirects. Presigned SeaweedFS URLs point at the
// network-internal host, so the request goes to the host endpoint with the original Host header.
func rawGet(t *testing.T, s *Server, tg *S3Target, path string, auth Auth) (int, []byte) {
	t.Helper()
	resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, path, nil), auth)
	for i := 0; i < 5 && resp.StatusCode >= 300 && resp.StatusCode < 400; i++ {
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("GET %s: redirect without location: %v", path, err)
		}
		req, err := http.NewRequest(http.MethodGet, loc.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if tg != nil {
			if ep, err := url.Parse(tg.Endpoint); err == nil && ep.Host == loc.Host {
				he, err := url.Parse(tg.HostEndpoint)
				if err != nil {
					t.Fatal(err)
				}
				req.URL.Scheme, req.URL.Host, req.Host = he.Scheme, he.Host, loc.Host
			}
		}
		resp, body = s.Do(t, req, nil)
	}
	return resp.StatusCode, body
}

func rawWantGet(t *testing.T, s *Server, tg *S3Target, path string, auth Auth, want []byte) {
	t.Helper()
	st, body := rawGet(t, s, tg, path, auth)
	if st != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200: %.200s", path, st, body)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("GET %s: got %d bytes, want %d bytes (content differs)", path, len(body), len(want))
	}
}

func rawWantStatus(t *testing.T, resp *http.Response, body []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, body)
	}
}

func TestRaw(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		tg := opts.S3
		repo := RandName("raw")
		// The repo denies overwrites until the subtest "overwrite allow" allows them.
		s.CreateRepo(t, repo, "raw", false, map[string]any{"overwrite": "deny"})

		writer, reader := RandName("w"), RandName("r")
		s.CreateUser(t, writer, "writer-pass-1234")
		s.CreateUser(t, reader, "reader-pass-1234")
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		s.SetPerms(t, repo, reader, "repo.download")
		writerAuth := s.PublishAuth(t, writer, repo)
		readerAuth := Basic{reader, s.CreateToken(t, reader, "reader-pass-1234", "ci")}

		data := rawRandom(t, 256<<10)
		path := "/raw/" + repo + "/dist/v1/app.bin"

		t.Run("curl upload and download", func(t *testing.T) {
			code, out := RunClient(t, curlImage, ClientOpts{
				Cmd: []string{"sh", "/work/run.sh"},
				Files: []ContainerFile{
					{Path: "/work/run.sh", Content: rawCurlScript, Mode: 0o755},
					{Path: "/work/app.bin", Content: string(data)},
				},
				Env: map[string]string{
					"RAW_URL":   s.InternalURL + path,
					"RAW_USER":  writerAuth.User,
					"RAW_TOKEN": writerAuth.Pass,
				},
				Timeout: 3 * time.Minute,
			})
			if code != 0 || !strings.Contains(out, "UPLOAD 201") || !strings.Contains(out, "RAW OK") {
				t.Fatalf("curl client: exit %d, want 0 with UPLOAD 201 and RAW OK:\n%s", code, out)
			}
		})

		t.Run("download with token", func(t *testing.T) {
			rawWantGet(t, s, tg, path, readerAuth, data)
		})

		t.Run("HEAD", func(t *testing.T) {
			resp, body := s.Do(t, s.NewRequest(t, http.MethodHead, path, nil), readerAuth)
			rawWantStatus(t, resp, body, http.StatusOK)
			if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(data)) {
				t.Fatalf("HEAD Content-Length = %q, want %d", cl, len(data))
			}
			sum := sha256.Sum256(data)
			if etag := resp.Header.Get("ETag"); etag != `"`+hex.EncodeToString(sum[:])+`"` {
				t.Fatalf("HEAD ETag = %q, want sha256 of the upload", etag)
			}
			if len(body) != 0 {
				t.Fatalf("HEAD returned a body of %d bytes", len(body))
			}
		})

		t.Run("unknown path is 404", func(t *testing.T) {
			resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+repo+"/dist/v1/missing.bin", nil), readerAuth)
			rawWantStatus(t, resp, body, http.StatusNotFound)
			resp, body = s.Do(t, s.NewRequest(t, http.MethodHead, "/raw/"+repo+"/dist/v1/missing.bin", nil), readerAuth)
			rawWantStatus(t, resp, body, http.StatusNotFound)
		})

		t.Run("anonymous, read token and password writes", func(t *testing.T) {
			resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, path, nil), nil)
			rawWantStatus(t, resp, body, http.StatusUnauthorized)
			resp, body = rawPut(t, s, "/raw/"+repo+"/anon.bin", []byte("x"), nil)
			rawWantStatus(t, resp, body, http.StatusUnauthorized)
			// Writes need a publish session: a read token or a password is refused.
			resp, body = rawPut(t, s, "/raw/"+repo+"/reader.bin", []byte("x"), readerAuth)
			rawWantStatus(t, resp, body, http.StatusUnauthorized)
			resp, body = rawPut(t, s, "/raw/"+repo+"/pw.bin", []byte("x"), Basic{writer, "writer-pass-1234"})
			rawWantStatus(t, resp, body, http.StatusUnauthorized)
			resp, body = rawPut(t, s, "/raw/"+repo+"/bad.bin", []byte("x"), Basic{writer, "not-a-token"})
			rawWantStatus(t, resp, body, http.StatusUnauthorized)

			pub := RandName("rawpub")
			s.CreateRepo(t, pub, "raw", true, nil)
			p := "/raw/" + pub + "/readme.txt"
			resp, body = rawPut(t, s, p, []byte("public"), s.PublishAuth(t, "admin", pub))
			rawWantStatus(t, resp, body, http.StatusCreated)
			rawWantGet(t, s, tg, p, nil, []byte("public"))
			resp, body = rawPut(t, s, p, []byte("changed"), nil)
			rawWantStatus(t, resp, body, http.StatusUnauthorized)
		})

		t.Run("overwrite deny", func(t *testing.T) {
			resp, body := rawPut(t, s, path, data, writerAuth)
			rawWantStatus(t, resp, body, http.StatusOK)
			resp, body = rawPut(t, s, path, []byte("different bytes"), writerAuth)
			rawWantStatus(t, resp, body, http.StatusConflict)
			rawWantGet(t, s, tg, path, readerAuth, data)
		})

		t.Run("overwrite allow", func(t *testing.T) {
			st, b := s.API(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"overwrite": "sometimes"}}, s.Admin())
			if st != http.StatusBadRequest {
				t.Fatalf("PATCH bad overwrite: status %d, want 400: %s", st, b)
			}
			st, b = s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("rawbad"), "format": "raw", "config": map[string]any{"overwrite": "yes"}}, s.Admin())
			if st != http.StatusBadRequest {
				t.Fatalf("create with bad overwrite: status %d, want 400: %s", st, b)
			}

			s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"overwrite": "allow"}}, s.Admin(), nil)
			next := rawRandom(t, 64<<10)
			resp, body := rawPut(t, s, path, next, writerAuth)
			rawWantStatus(t, resp, body, http.StatusOK)
			rawWantGet(t, s, tg, path, readerAuth, next)
			resp, body = rawPut(t, s, path, next, writerAuth)
			rawWantStatus(t, resp, body, http.StatusOK)
		})

		t.Run("wrong format is 404", func(t *testing.T) {
			mvn := RandName("mvn")
			s.CreateRepo(t, mvn, "maven", false, nil)
			resp, body := rawPut(t, s, "/raw/"+mvn+"/file.bin", []byte("x"), s.PublishAuth(t, "admin", mvn))
			rawWantStatus(t, resp, body, http.StatusNotFound)
		})

		t.Run("usage snippets", func(t *testing.T) {
			var u struct {
				BaseURL  string `json:"base_url"`
				Snippets []struct {
					Title   string `json:"title"`
					Purpose string `json:"purpose"`
					Via     string `json:"via"`
					Lang    string `json:"lang"`
					Code    string `json:"code"`
				} `json:"snippets"`
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/usage", nil, readerAuth, &u)
			if u.BaseURL != s.InternalURL {
				t.Fatalf("usage base_url = %q, want %q", u.BaseURL, s.InternalURL)
			}
			want := s.InternalURL + "/raw/" + repo + "/"
			var upload, download, ci bool
			for _, sn := range u.Snippets {
				if sn.Purpose == "use" && !strings.Contains(sn.Code, want) {
					t.Fatalf("snippet %q does not reference %s: %s", sn.Title, want, sn.Code)
				}
				upload = upload || sn.Via == "computer" && strings.Contains(sn.Code, "telesphorepo publish ")
				ci = ci || sn.Via == "ci" && strings.Contains(sn.Code, s.InternalURL+"/install.sh") && strings.Contains(sn.Code, "telesphorepo publish ")
				download = download || strings.Contains(sn.Code, "curl") && sn.Purpose == "use"
			}
			if !upload || !download || !ci {
				b, _ := json.Marshal(u)
				t.Fatalf("usage: want a curl download, a CLI publish and a CI file with the install step, got %s", b)
			}
		})
	})
}
