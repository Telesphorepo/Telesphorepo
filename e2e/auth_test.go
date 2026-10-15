//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const authMavenImage = "maven:3.9.16-eclipse-temurin-21"

func authPut(t *testing.T, s *Server, path string, body string, auth Auth) (*http.Response, []byte) {
	t.Helper()
	return s.Do(t, s.NewRequest(t, http.MethodPut, path, strings.NewReader(body)), auth)
}

func authGet(t *testing.T, s *Server, path string, auth Auth) (*http.Response, []byte) {
	t.Helper()
	return s.Do(t, s.NewRequest(t, http.MethodGet, path, nil), auth)
}

func wantStatus(t *testing.T, resp *http.Response, body []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, body)
	}
}

func wantChallenge(t *testing.T, resp *http.Response) {
	t.Helper()
	if h := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(h, "Basic") {
		t.Fatalf("WWW-Authenticate = %q, want Basic challenge", h)
	}
}

func TestAuth(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("auth")
	s.CreateRepo(t, repo, "maven", false, nil)

	const artifact = "/maven/%s/com/example/auth/1.0/auth-1.0.txt"
	path := strings.Replace(artifact, "%s", repo, 1)
	resp, body := authPut(t, s, path, "hello", s.PublishAuth(t, "admin", repo))
	wantStatus(t, resp, body, http.StatusCreated)

	t.Run("wrong password on PUT", func(t *testing.T) {
		resp, body := authPut(t, s, path+".new", "x", Basic{"admin", "not-the-password"})
		wantStatus(t, resp, body, http.StatusUnauthorized)
		wantChallenge(t, resp)
	})

	t.Run("anonymous GET on private repo", func(t *testing.T) {
		resp, body := authGet(t, s, path, nil)
		wantStatus(t, resp, body, http.StatusUnauthorized)
		wantChallenge(t, resp)
	})

	t.Run("anonymous read toggle", func(t *testing.T) {
		anon := RandName("anon")
		s.CreateRepo(t, anon, "maven", true, nil)
		p := "/maven/" + anon + "/com/example/anon/1.0/anon-1.0.txt"
		resp, body := authPut(t, s, p, "public", s.PublishAuth(t, "admin", anon))
		wantStatus(t, resp, body, http.StatusCreated)

		resp, body = authGet(t, s, p, nil)
		wantStatus(t, resp, body, http.StatusOK)
		if string(body) != "public" {
			t.Fatalf("body = %q", body)
		}
		resp, body = authPut(t, s, p+".x", "nope", nil)
		wantStatus(t, resp, body, http.StatusUnauthorized)

		s.MustAPI(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{anon}, "anonymous_read": false}, s.Admin(), nil)
		resp, body = authGet(t, s, p, nil)
		wantStatus(t, resp, body, http.StatusUnauthorized)
		wantChallenge(t, resp)
	})

	t.Run("user without permission", func(t *testing.T) {
		u := RandName("noperm")
		s.CreateUser(t, u, "pw-"+randHex(6))
		pw := "pw-" + randHex(6)
		s.MustAPI(t, http.MethodPatch, "/api/users/"+u, map[string]any{"password": pw}, s.Admin(), nil)
		resp, body := authGet(t, s, path, Basic{u, pw})
		wantStatus(t, resp, body, http.StatusForbidden)
		// Writes need a publish session; a password is refused before any permission check.
		resp, body = authPut(t, s, path+".y", "x", Basic{u, pw})
		wantStatus(t, resp, body, http.StatusUnauthorized)
		wantChallenge(t, resp)
	})

	t.Run("read-only user", func(t *testing.T) {
		u, pw := RandName("reader"), "pw-"+randHex(8)
		s.CreateUser(t, u, pw)
		s.SetPerms(t, repo, u, "download")
		resp, body := authGet(t, s, path, Basic{u, pw})
		wantStatus(t, resp, body, http.StatusOK)
		resp, body = authPut(t, s, path+".z", "x", Basic{u, pw})
		wantStatus(t, resp, body, http.StatusUnauthorized)
		// The user cannot mint a session for a repo it only reads.
		if st, b := s.Mint(t, s.EnrollDevice(t, u), repo); st != http.StatusForbidden {
			t.Fatalf("mint a publish session as a read-only user: status %d, want 403: %s", st, b)
		}

		group := "com.example.authdeploy" + randHex(4)
		settings := `<settings><servers><server><id>tr</id><username>` + u + `</username><password>` + pw + `</password></server></servers>
<mirrors><mirror><id>maven-default-http-blocker</id><mirrorOf>external:dummy:*</mirrorOf><url>http://0.0.0.0/</url><blocked>false</blocked></mirror></mirrors></settings>`
		pom := `<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion>
<groupId>` + group + `</groupId><artifactId>demo</artifactId><version>1.0</version><packaging>pom</packaging>
<distributionManagement><repository><id>tr</id><url>` + s.InternalURL + `/maven/` + repo + `</url></repository></distributionManagement></project>`
		code, out := RunClient(t, authMavenImage, ClientOpts{
			Cmd:        []string{"mvn", "-B", "-s", "/settings.xml", "deploy"},
			Files:      []ContainerFile{{Content: settings, Path: "/settings.xml"}, {Content: pom, Path: "/proj/pom.xml"}},
			Mounts:     map[string]string{M2Volume: "/root/.m2"},
			WorkingDir: "/proj",
		})
		if code == 0 {
			t.Fatalf("mvn deploy as read-only user succeeded:\n%s", out)
		}
		if !strings.Contains(out, "401") {
			t.Fatalf("mvn output lacks 401:\n%s", out)
		}
	})

	t.Run("token auth", func(t *testing.T) {
		u, pw := RandName("tok"), "pw-"+randHex(8)
		s.CreateUser(t, u, pw)
		s.SetPerms(t, repo, u, "download")
		tok := s.CreateToken(t, u, pw, "ci")
		for _, name := range []string{"x", "anything", u} {
			resp, body := authGet(t, s, path, Basic{name, tok})
			wantStatus(t, resp, body, http.StatusOK)
		}
	})

	t.Run("revoked token", func(t *testing.T) {
		u, pw := RandName("revoke"), "pw-"+randHex(8)
		s.CreateUser(t, u, pw)
		s.SetPerms(t, repo, u, "download")
		var tk struct {
			ID    json.RawMessage `json:"id"`
			Token string          `json:"token"`
		}
		sess := s.SignIn(t, u, pw)
		s.MustAPI(t, http.MethodPost, "/api/tokens", map[string]any{"name": "tmp"}, sess, &tk)
		resp, body := authGet(t, s, path, Basic{"x", tk.Token})
		wantStatus(t, resp, body, http.StatusOK)
		id := strings.Trim(string(tk.ID), `"`)
		st, b := s.API(t, http.MethodDelete, "/api/tokens/"+id, nil, sess)
		if st != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", st, b)
		}
		resp, body = authGet(t, s, path, Basic{"x", tk.Token})
		wantStatus(t, resp, body, http.StatusUnauthorized)
	})

	t.Run("disabled user", func(t *testing.T) {
		u, pw := RandName("disabled"), "pw-"+randHex(8)
		s.CreateUser(t, u, pw)
		s.SetPerms(t, repo, u, "download")
		resp, body := authGet(t, s, path, Basic{u, pw})
		wantStatus(t, resp, body, http.StatusOK)
		s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{u}, "disabled": true}, s.Admin(), nil)
		resp, body = authGet(t, s, path, Basic{u, pw})
		wantStatus(t, resp, body, http.StatusUnauthorized)
	})

	t.Run("nonexistent repo", func(t *testing.T) {
		p := "/maven/" + RandName("missing") + "/a/b/1/b-1.jar"
		resp, body := authGet(t, s, p, nil)
		wantStatus(t, resp, body, http.StatusUnauthorized)
		resp, body = authGet(t, s, p, s.AdminBasic())
		wantStatus(t, resp, body, http.StatusNotFound)
	})

	t.Run("password change invalidates old password", func(t *testing.T) {
		u, oldPw, newPw := RandName("chpw"), "old-"+randHex(8), "new-"+randHex(8)
		s.CreateUser(t, u, oldPw)
		s.SetPerms(t, repo, u, "download")
		for range 2 { // second request is served from the verification cache
			resp, body := authGet(t, s, path, Basic{u, oldPw})
			wantStatus(t, resp, body, http.StatusOK)
		}
		s.MustAPI(t, http.MethodPatch, "/api/users/"+u, map[string]any{"password": newPw}, s.Admin(), nil)
		resp, body := authGet(t, s, path, Basic{u, oldPw})
		wantStatus(t, resp, body, http.StatusUnauthorized)
		resp, body = authGet(t, s, path, Basic{u, newPw})
		wantStatus(t, resp, body, http.StatusOK)
	})
}

func TestAuthRateLimit(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("rl")
	s.CreateRepo(t, repo, "maven", false, nil)
	u, pw := RandName("victim"), "pw-"+randHex(8)
	s.CreateUser(t, u, pw)
	s.SetPerms(t, repo, u, "download")
	path := "/maven/" + repo + "/a/b/1/b-1.txt"

	var limited *http.Response
	for i := 0; i < 12; i++ {
		resp, _ := authGet(t, s, path, Basic{u, "wrong-" + randHex(4)})
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = resp
			break
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401 or 429", i, resp.StatusCode)
		}
	}
	if limited == nil {
		t.Fatal("no 429 after 12 failed attempts")
	}
	if limited.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	resp, body := authGet(t, s, path, Basic{u, pw})
	wantStatus(t, resp, body, http.StatusTooManyRequests)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 for correct password without Retry-After")
	}
}

// TestNoBrowserPasswordPrompt: no answer on /api and no answer to a request of the web UI
// (X-Telesphorepo-UI: 1) carries WWW-Authenticate, so a browser never opens its password dialog.
// Format routes keep the challenge for package clients.
func TestNoBrowserPasswordPrompt(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("prompt")
	s.CreateRepo(t, repo, "maven", false, nil)
	artifact := "/maven/" + repo + "/com/example/p/1.0/p-1.0.txt"
	ui := map[string]string{"X-Telesphorepo-UI": "1"}

	noChallenge := func(what string, method, path string, auth Auth, hdr map[string]string) {
		t.Helper()
		req := s.NewRequest(t, method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, body := s.Do(t, req, auth)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401: %s", what, resp.StatusCode, body)
		}
		if h := resp.Header.Get("WWW-Authenticate"); h != "" {
			t.Fatalf("%s: WWW-Authenticate = %q, want none", what, h)
		}
	}
	noChallenge("API with a wrong password", http.MethodGet, "/api/me", Basic{"admin", "not-the-password"}, nil)
	noChallenge("API anonymous", http.MethodGet, "/api/me", nil, nil)
	noChallenge("API repo files anonymous", http.MethodGet, "/api/repos/"+repo+"/tree?path=", nil, nil)
	noChallenge("API upload without a session", http.MethodPost, "/api/repos/"+repo+"/upload", Basic{"admin", "not-the-password"}, nil)
	noChallenge("UI download of a private file", http.MethodGet, artifact, nil, ui)
	noChallenge("UI download with a wrong password", http.MethodGet, artifact, Basic{"admin", "not-the-password"}, ui)
	noChallenge("UI upload without a session", http.MethodPut, artifact, nil, ui)

	// Without the UI header a format route keeps the Basic challenge.
	resp, body := authGet(t, s, artifact, nil)
	wantStatus(t, resp, body, http.StatusUnauthorized)
	wantChallenge(t, resp)
}
