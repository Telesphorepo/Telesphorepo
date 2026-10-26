//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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

// rawPutIf PUTs body to path with the extra headers h.
func rawPutIf(t *testing.T, s *Server, path string, body []byte, auth Auth, h map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := s.NewRequest(t, http.MethodPut, path, bytes.NewReader(body))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	return s.Do(t, req, auth)
}

// rawETag is the ETag of a file of bytes b: its SHA-256 in quotes.
func rawETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// rawWantPrecondition checks a 412 of a format route for file of repo: the error names the file,
// the code is precondition_failed and etag is etag ("": null).
func rawWantPrecondition(t *testing.T, resp *http.Response, body []byte, repo, file, etag string) {
	t.Helper()
	rawWantStatus(t, resp, body, http.StatusPreconditionFailed)
	var out struct {
		Error string  `json:"error"`
		Code  string  `json:"code"`
		ETag  *string `json:"etag"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("412 of %s/%s: %v: %s", repo, file, err, body)
	}
	got := ""
	if out.ETag != nil {
		got = *out.ETag
	}
	if !strings.HasPrefix(out.Error, file+" changed: ") || out.Code != "precondition_failed" || got != etag || (etag == "") != (out.ETag == nil) {
		t.Fatalf("412 of %s/%s: %s, want the error %q..., the code precondition_failed and the etag %q", repo, file, body, file+" changed: ", etag)
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
			// The precondition comes before the overwrite policy: an If-Match that names the
			// file does not let other bytes replace it.
			resp, body = rawPutIf(t, s, path, []byte("different bytes"), writerAuth, map[string]string{"If-Match": rawETag(data)})
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

		t.Run("conditional put", func(t *testing.T) {
			cond := RandName("rawcond")
			s.CreateRepo(t, cond, "raw", false, map[string]any{"overwrite": "allow"})
			s.SetPerms(t, cond, writer, "repo.download", "repo.publish")
			sessions := [2]Basic{s.PublishAuth(t, writer, cond), s.PublishAuth(t, writer, cond)}
			latest := "/raw/" + cond + "/v1/latest.json"
			v0 := []byte(`{"build":0}`)

			// If-None-Match: * creates the file, then refuses a write of any bytes, the same too.
			resp, body := rawPutIf(t, s, latest, v0, sessions[0], map[string]string{"If-None-Match": "*"})
			rawWantStatus(t, resp, body, http.StatusCreated)
			if got := resp.Header.Get("ETag"); got != rawETag(v0) {
				t.Fatalf("PUT If-None-Match: ETag %q, want %q", got, rawETag(v0))
			}
			resp, body = rawPutIf(t, s, latest, v0, sessions[0], map[string]string{"If-None-Match": "*"})
			rawWantPrecondition(t, resp, body, cond, "v1/latest.json", rawETag(v0))
			// If-Match on a path without a file: 412 with the etag null.
			resp, body = rawPutIf(t, s, "/raw/"+cond+"/v1/none.json", v0, sessions[0], map[string]string{"If-Match": rawETag(v0)})
			rawWantPrecondition(t, resp, body, cond, "v1/none.json", "")
			resp, body = rawPutIf(t, s, "/raw/"+cond+"/v1/none.json", v0, sessions[0], map[string]string{"If-Match": "*"})
			rawWantPrecondition(t, resp, body, cond, "v1/none.json", "")
			// A condition that a write does not take is 400, never ignored.
			for _, h := range []map[string]string{
				{"If-None-Match": rawETag(v0)},
				{"If-Unmodified-Since": time.Now().UTC().Format(http.TimeFormat)},
				{"If-Match": "W/" + rawETag(v0)},
				{"If-Match": "*, " + rawETag(v0)},
			} {
				resp, body = rawPutIf(t, s, latest, v0, sessions[0], h)
				rawWantStatus(t, resp, body, http.StatusBadRequest)
			}

			// Two publish sessions race If-Match from the same ETag: in each round one write
			// stores, and the other is 412 with the ETag of the winner.
			cur, last := rawETag(v0), v0
			for round := 1; round <= 20; round++ {
				var wg sync.WaitGroup
				var codes [2]int
				var etags [2]string
				var errs [2]error
				bodies := [2][]byte{}
				for i := range sessions {
					bodies[i] = []byte(fmt.Sprintf(`{"build":%d,"by":%d}`, round, i))
					req := s.NewRequest(t, http.MethodPut, latest, bytes.NewReader(bodies[i]))
					req.Header.Set("If-Match", cur)
					sessions[i].apply(req)
					wg.Go(func() {
						resp, err := s.HTTPClient(sessions[i]).Do(req)
						if err != nil {
							errs[i] = err
							return
						}
						defer resp.Body.Close()
						var out struct {
							ETag *string `json:"etag"`
						}
						codes[i], etags[i] = resp.StatusCode, resp.Header.Get("ETag")
						if resp.StatusCode == http.StatusPreconditionFailed {
							if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ETag == nil {
								errs[i] = fmt.Errorf("412 without an etag: %v", err)
								return
							}
							etags[i] = *out.ETag
						}
					})
				}
				wg.Wait()
				for _, err := range errs {
					if err != nil {
						t.Fatalf("round %d: %v", round, err)
					}
				}
				win := 0
				if codes[1] == http.StatusOK {
					win = 1
				}
				lose := 1 - win
				if codes[win] != http.StatusOK || codes[lose] != http.StatusPreconditionFailed {
					t.Fatalf("round %d: statuses %v, want one 200 and one 412", round, codes)
				}
				if etags[win] != rawETag(bodies[win]) || etags[lose] != etags[win] {
					t.Fatalf("round %d: winner ETag %q (want %q), loser's etag %q, want the winner's", round, etags[win], rawETag(bodies[win]), etags[lose])
				}
				cur, last = etags[win], bodies[win]
			}
			rawWantGet(t, s, tg, latest, Basic{writer, "writer-pass-1234"}, last)

			// If-Match that names the file: the write stores, and answers the new ETag.
			next := []byte(`{"build":21}`)
			resp, body = rawPutIf(t, s, latest, next, sessions[1], map[string]string{"If-Match": `"` + strings.Repeat("0", 64) + `", ` + cur})
			rawWantStatus(t, resp, body, http.StatusOK)
			if got := resp.Header.Get("ETag"); got != rawETag(next) {
				t.Fatalf("PUT If-Match list: ETag %q, want %q", got, rawETag(next))
			}
		})

		t.Run("path rules", func(t *testing.T) {
			name := RandName("rawrules")
			// The repo replaces files, except under builds/, which keeps every build at any depth.
			s.CreateRepo(t, name, "raw", false, map[string]any{"overwrite": "allow", "rules": []map[string]string{
				{"match": "builds/**", "overwrite": "deny"}, {"match": "latest.json", "overwrite": "allow"},
			}})
			s.SetPerms(t, name, writer, "repo.download", "repo.publish")
			pub := s.PublishAuth(t, writer, name)
			build, latest := "/raw/"+name+"/builds/7/linux/app.jar", "/raw/"+name+"/latest.json"
			jar := rawRandom(t, 4<<10)
			resp, body := rawPut(t, s, build, jar, pub)
			rawWantStatus(t, resp, body, http.StatusCreated)
			resp, body = rawPut(t, s, build, jar, pub)
			rawWantStatus(t, resp, body, http.StatusOK)
			resp, body = rawPut(t, s, build, []byte("other bytes"), pub)
			rawWantStatus(t, resp, body, http.StatusConflict)
			for i, v := range []string{`{"build":6}`, `{"build":7}`} {
				resp, body = rawPut(t, s, latest, []byte(v), pub)
				rawWantStatus(t, resp, body, []int{http.StatusCreated, http.StatusOK}[i])
			}
			rawWantGet(t, s, tg, latest, Basic{writer, "writer-pass-1234"}, []byte(`{"build":7}`))
			rawWantGet(t, s, tg, build, Basic{writer, "writer-pass-1234"}, jar)

			// A rule written wrong is refused with its number.
			for _, bad := range []map[string]string{{"match": "/builds/**", "overwrite": "deny"}, {"match": "a//b", "overwrite": "deny"}, {"match": "x", "overwrite": "never"}} {
				st, b := s.API(t, http.MethodPatch, "/api/repos/"+name, map[string]any{"config": map[string]any{"overwrite": "allow", "rules": []map[string]string{{"match": "ok/**", "overwrite": "deny"}, bad}}}, s.Admin())
				if st != http.StatusBadRequest || !strings.Contains(string(b), "rule 2") {
					t.Fatalf("PATCH rule %v: status %d, want 400 naming rule 2: %s", bad, st, b)
				}
			}
			many := make([]map[string]string, 101)
			for i := range many {
				many[i] = map[string]string{"match": fmt.Sprintf("r%d/**", i), "overwrite": "deny"}
			}
			if st, b := s.API(t, http.MethodPatch, "/api/repos/"+name, map[string]any{"config": map[string]any{"overwrite": "allow", "rules": many}}, s.Admin()); st != http.StatusBadRequest {
				t.Fatalf("PATCH 101 rules: status %d, want 400: %s", st, b)
			}

			// A change of the rules applies to the next write.
			s.MustAPI(t, http.MethodPatch, "/api/repos/"+name, map[string]any{"config": map[string]any{"overwrite": "allow"}}, s.Admin(), nil)
			resp, body = rawPut(t, s, build, []byte("other bytes"), pub)
			rawWantStatus(t, resp, body, http.StatusOK)
			s.MustAPI(t, http.MethodPatch, "/api/repos/"+name, map[string]any{"config": map[string]any{"overwrite": "allow", "rules": []map[string]string{{"match": "**", "overwrite": "deny"}}}}, s.Admin(), nil)
			resp, body = rawPut(t, s, latest, []byte(`{"build":8}`), pub)
			rawWantStatus(t, resp, body, http.StatusConflict)
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
