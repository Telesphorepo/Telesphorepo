//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

// conditionalGet sends GET path with If-None-Match etag (and Accept accept when not "") and
// returns the status and the ETag of the answer.
func conditionalGet(t *testing.T, s *Server, path, accept, etag string, auth Auth) (int, string) {
	t.Helper()
	req := s.NewRequest(t, http.MethodGet, path, nil)
	req.Header.Set("If-None-Match", etag)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, _ := s.Do(t, req, auth)
	return resp.StatusCode, resp.Header.Get("ETag")
}

// TestCacheControl: a format read of a repo that not everyone reads answers with
// Cache-Control: private (GET, HEAD and 304), so a shared cache in front of the server does not
// keep it; a public repo answers with no Cache-Control. A public group with a private member, and
// a public repo or member with a read IP rule, count as private.
func TestCacheControl(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		priv, pub := RandName("cpriv"), RandName("cpub")
		mixed, open := RandName("cmix"), RandName("copen")
		s.CreateRepo(t, priv, "maven", false, nil)
		s.CreateRepo(t, pub, "maven", true, nil)
		s.CreateGroup(t, mixed, "maven", true, []string{pub, priv})
		s.CreateGroup(t, open, "maven", true, []string{pub})
		const user, pass = "cachereader", "cachereader-pass-1234"
		s.CreateUser(t, user, pass)
		s.SetPerms(t, priv, user, "repo.download")
		reader := Basic{user, pass}

		privJar, pubJar := "com/example/secret/1.0/secret-1.0.jar", "com/example/open/1.0/open-1.0.jar"
		privBody, pubBody := rawRandom(t, 4096), rawRandom(t, 4096)
		mavenPut(t, s, s.AdminUser, priv, privJar, privBody)
		mavenPut(t, s, s.AdminUser, pub, pubJar, pubBody)

		check := func(t *testing.T, what, path string, auth Auth, want string) {
			t.Helper()
			resp, body := s.Do(t, s.NewRequest(t, http.MethodHead, path, nil), auth)
			etag := resp.Header.Get("ETag")
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != want || etag == "" {
				t.Fatalf("HEAD %s: status %d, Cache-Control %q, ETag %q, want 200, %q and an ETag: %s", what, resp.StatusCode, resp.Header.Get("Cache-Control"), etag, want, body)
			}
			req := s.NewRequest(t, http.MethodGet, path, nil)
			req.Header.Set("If-None-Match", etag)
			resp, body = s.Do(t, req, auth)
			if resp.StatusCode != http.StatusNotModified || resp.Header.Get("Cache-Control") != want {
				t.Fatalf("conditional GET %s: status %d, Cache-Control %q, want 304 and %q: %s", what, resp.StatusCode, resp.Header.Get("Cache-Control"), want, body)
			}
			resp, body = s.Do(t, s.NewRequest(t, http.MethodGet, path, nil), auth)
			switch {
			case resp.StatusCode == http.StatusOK && resp.Header.Get("Cache-Control") == want:
			case resp.StatusCode == http.StatusFound && resp.Header.Get("Cache-Control") == "no-store":
				// A presigned redirect of the S3 store is never cached.
			default:
				t.Fatalf("GET %s: status %d, Cache-Control %q, want 200 with %q or a 302 with no-store: %.200s", what, resp.StatusCode, resp.Header.Get("Cache-Control"), want, body)
			}
		}
		check(t, "a file of a private repo", "/maven/"+priv+"/"+privJar, reader, "private")
		check(t, "a file of a public repo", "/maven/"+pub+"/"+pubJar, nil, "")
		check(t, "a private member's file through a public group", "/maven/"+mixed+"/"+privJar, reader, "private")
		check(t, "a public member's file through a group with a private member", "/maven/"+mixed+"/"+pubJar, nil, "private")
		check(t, "a file of a group of public members", "/maven/"+open+"/"+pubJar, nil, "")

		// A read IP rule makes a public repo private: not every address reads it. The rule allows
		// every address, so the test client reads.
		putIPRules(t, s, pub, ipRules{Read: []ipRule{{Kind: "cidr", Value: "0.0.0.0/0"}, {Kind: "cidr", Value: "::/0"}}, Publish: []ipRule{}})
		check(t, "a file of a public repo with a read IP rule", "/maven/"+pub+"/"+pubJar, nil, "private")
		check(t, "a file of a group whose public member has a read IP rule", "/maven/"+open+"/"+pubJar, nil, "private")
	})
}
