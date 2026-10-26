//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestRepoDomains: a repo domain (repo_domains) serves the files of its repo at its root, only
// reads, keeps the access of the repo without the UI session cookie, follows a rename of its repo,
// and serves a Maven repo the same way; the setting refuses a Docker repo and an address of the
// server itself. A host that the server does not answer for gets 421, except /healthz.
func TestRepoDomains(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	pub, priv, mvn := RandName("updates"), RandName("private"), RandName("maven")
	s.CreateRepo(t, pub, "raw", true, nil)
	s.CreateRepo(t, priv, "raw", false, nil)
	s.CreateRepo(t, mvn, "maven", true, nil)
	writer, reader := RandName("w"), RandName("r")
	s.CreateUser(t, writer, "writer-pass-1234")
	s.CreateUser(t, reader, "reader-pass-1234")
	for _, r := range []string{pub, priv, mvn} {
		s.SetPerms(t, r, writer, "repo.download", "repo.publish")
	}
	s.SetPerms(t, priv, reader, "repo.download")

	put := func(path string, body []byte) {
		t.Helper()
		repo := strings.Split(path, "/")[2]
		resp, b := s.Do(t, s.NewRequest(t, http.MethodPut, path, bytes.NewReader(body)), s.PublishAuth(t, writer, repo))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s: status %d: %s", path, resp.StatusCode, b)
		}
	}
	latest := []byte(`{"version":"1.2.3"}`)
	build := rawRandom(t, 4096)
	jar := rawRandom(t, 2048)
	put("/raw/"+pub+"/v1/latest.json", latest)
	put("/raw/"+pub+"/builds/7/app.bin", build)
	put("/raw/"+priv+"/v1/latest.json", latest)
	put("/maven/"+mvn+"/com/acme/lib/1.0/lib-1.0.jar", jar)

	pubHost, privHost, mvnHost := pub+".example.com", priv+".example.com", mvn+".example.org"
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"repo_domains": []string{
		strings.ToUpper(pubHost) + " = " + pub, privHost + "=" + priv, mvnHost + "=" + mvn,
	}}, s.Admin(), nil)

	// at sends method path with Host host.
	at := func(host, method, path string, auth Auth) (*http.Response, []byte) {
		t.Helper()
		req := s.NewRequest(t, method, path, nil)
		req.Host = host
		return s.Do(t, req, auth)
	}
	want := func(what string, resp *http.Response, b []byte, status int) {
		t.Helper()
		if resp.StatusCode != status {
			t.Fatalf("%s: status %d, want %d: %s", what, resp.StatusCode, status, b)
		}
	}

	t.Run("public repo at the root", func(t *testing.T) {
		resp, b := at(pubHost+":8080", http.MethodGet, "/v1/latest.json", nil)
		want(t.Name(), resp, b, http.StatusOK)
		if !bytes.Equal(b, latest) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("GET /v1/latest.json: %q as %q, want the file as application/json", b, resp.Header.Get("Content-Type"))
		}
		resp, b = at(pubHost, http.MethodGet, "/builds/7/app.bin", nil)
		want("GET a build", resp, b, http.StatusOK)
		if !bytes.Equal(b, build) {
			t.Fatal("GET /builds/7/app.bin: other bytes")
		}
		resp, b = at(pubHost, http.MethodHead, "/builds/7/app.bin", nil)
		want("HEAD a build", resp, b, http.StatusOK)

		resp, b = at(pubHost, http.MethodGet, "/v1/missing.json", nil)
		want("a missing path", resp, b, http.StatusNotFound)
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) != nil || e.Error != "not found" {
			t.Fatalf("a missing path: %s, want {\"error\":\"not found\"}", b)
		}
		// No web UI on a domain: "/" and the UI's files are paths of the repo.
		for _, p := range []string{"/", "/index.html"} {
			resp, b = at(pubHost, http.MethodGet, p, nil)
			want("GET "+p, resp, b, http.StatusNotFound)
			if strings.Contains(strings.ToLower(string(b)), "<html") {
				t.Fatalf("GET %s on the domain served a page: %s", p, b)
			}
		}
		// The route table's clean path, on the domain.
		resp, b = at(pubHost, http.MethodGet, "/v1/../v1/latest.json", nil)
		want("an unclean path", resp, b, http.StatusMovedPermanently)
		if loc := resp.Header.Get("Location"); loc != "/v1/latest.json" {
			t.Fatalf("an unclean path redirects to %q, want /v1/latest.json", loc)
		}
	})

	t.Run("only reads", func(t *testing.T) {
		req := s.NewRequest(t, http.MethodPut, "/v1/latest.json", bytes.NewReader([]byte(`{}`)))
		req.Host = pubHost
		resp, b := s.Do(t, req, s.PublishAuth(t, writer, pub))
		want("PUT on the domain", resp, b, http.StatusMethodNotAllowed)
		if a := resp.Header.Get("Allow"); a != "GET, HEAD" {
			t.Fatalf("PUT on the domain: Allow %q, want GET, HEAD", a)
		}
	})

	t.Run("private repo", func(t *testing.T) {
		resp, b := at(privHost, http.MethodGet, "/v1/latest.json", nil)
		want("anonymous", resp, b, http.StatusUnauthorized)
		if ch := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(ch, "Basic ") {
			t.Fatalf("anonymous: WWW-Authenticate %q, want a Basic challenge", ch)
		}
		token := s.CreateToken(t, reader, "reader-pass-1234", "updater")
		resp, b = at(privHost, http.MethodGet, "/v1/latest.json", Basic{reader, token})
		want("Basic with a read token", resp, b, http.StatusOK)
		if !bytes.Equal(b, latest) {
			t.Fatalf("Basic with a read token: %q", b)
		}
		req := s.NewRequest(t, http.MethodGet, "/v1/latest.json", nil)
		req.Host = privHost
		req.Header.Set("Authorization", "Bearer "+token)
		resp, b = s.Do(t, req, nil)
		want("Bearer read token", resp, b, http.StatusOK)

		// A valid UI session reads on the base URL, and is no credential on the domain.
		sess := s.SignIn(t, reader, "reader-pass-1234")
		resp, b = s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+priv+"/v1/latest.json", nil), sess)
		want("the UI session on the base URL", resp, b, http.StatusOK)
		resp, b = at(privHost, http.MethodGet, "/v1/latest.json", sess)
		want("the UI session on the domain", resp, b, http.StatusUnauthorized)
		if ch := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(ch, "Basic ") {
			t.Fatalf("the UI session on the domain: WWW-Authenticate %q, want a Basic challenge", ch)
		}
	})

	t.Run("maven repo", func(t *testing.T) {
		resp, b := at(mvnHost, http.MethodGet, "/com/acme/lib/1.0/lib-1.0.jar", nil)
		want(t.Name(), resp, b, http.StatusOK)
		if !bytes.Equal(b, jar) {
			t.Fatal("GET the jar on the Maven domain: other bytes")
		}
	})

	t.Run("a renamed repo", func(t *testing.T) {
		renamed := RandName("renamed")
		s.MustAPI(t, http.MethodPost, "/api/repos/"+mvn+"/rename", map[string]any{"name": renamed}, s.Admin(), nil)
		resp, b := at(mvnHost, http.MethodGet, "/com/acme/lib/1.0/lib-1.0.jar", nil)
		want("GET through the old name of the repo", resp, b, http.StatusOK)
	})

	t.Run("the setting", func(t *testing.T) {
		img := RandName("images")
		s.CreateRepo(t, img, "docker", true, nil)
		upload := RandName("upload") + ".example.com"
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.upload_url": "https://" + upload}, s.Admin(), nil)
		for _, c := range []struct{ what, line, says string }{
			{"a Docker repo", img + ".example.com=" + img, "Docker"},
			{"no such repo", "nothing.example.com=" + RandName("nothing"), "there is no repository"},
			{"the Docker upload host", upload + "=" + pub, "an address of this server itself"},
			{"a port", "files.example.com:8443=" + pub, "not a domain name"},
			{"a loopback name", "localhost=" + pub, "not a domain name"},
		} {
			st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"repo_domains": []string{pubHost + "=" + pub, c.line}}, s.Admin())
			if st != http.StatusBadRequest || !strings.Contains(string(b), c.says) || !strings.Contains(string(b), "repo_domains") {
				t.Fatalf("%s: status %d: %s, want 400 on repo_domains saying %q", c.what, st, b, c.says)
			}
		}
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.upload_url": ""}, s.Admin(), nil)
		// The domains that the refused changes named still answer as before.
		resp, b := at(pubHost, http.MethodGet, "/v1/latest.json", nil)
		want("the domain after the refused changes", resp, b, http.StatusOK)
	})

	t.Run("other hosts", func(t *testing.T) {
		resp, b := at("unknown.example.com", http.MethodGet, "/raw/"+pub+"/v1/latest.json", nil)
		want("an unknown host", resp, b, http.StatusMisdirectedRequest)
		if string(b) != "This server does not answer for this host.\n" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Fatalf("an unknown host: %q as %q", b, resp.Header.Get("Content-Type"))
		}
		resp, b = at("unknown.example.com", http.MethodGet, "/", nil)
		want("the UI on an unknown host", resp, b, http.StatusMisdirectedRequest)
		resp, b = at("unknown.example.com", http.MethodGet, "/healthz", nil)
		want("/healthz on an unknown host", resp, b, http.StatusOK)
		// The base URL host and the loopback names answer as before.
		for _, h := range []string{s.Alias + ":8080", "127.0.0.1:8080", "[::1]:8080"} {
			resp, b = at(h, http.MethodGet, "/raw/"+pub+"/v1/latest.json", nil)
			want("GET at "+h, resp, b, http.StatusOK)
		}
	})
}
