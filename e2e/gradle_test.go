//go:build e2e

package e2e

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"testing"
)

// TestGradleModule publishes the files that Gradle's maven-publish adds to a Maven deploy: the
// Gradle module metadata (.module) and the .sha256 and .sha512 checksum files, which the server
// stores as sent.
func TestGradleModule(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("gradle")
		s.CreateRepo(t, repo, "maven", false, nil)
		pub := s.PublishAuth(t, s.AdminUser, repo)

		base := "/maven/" + repo + "/com/example/e2e" + randHex(4) + "/greet/1.0/greet-1.0.module"
		module := []byte(`{"formatVersion":"1.1","component":{"group":"com.example","module":"greet","version":"1.0"},"variants":[]}`)
		s256, s512 := sha256.Sum256(module), sha512.Sum512(module)
		for _, f := range []struct {
			path string
			body []byte
		}{
			{base, module},
			{base + ".sha256", []byte(hex.EncodeToString(s256[:]))},
			{base + ".sha512", []byte(hex.EncodeToString(s512[:]))},
		} {
			resp, body := rawPut(t, s, f.path, f.body, pub)
			rawWantStatus(t, resp, body, http.StatusCreated)
			rawWantGet(t, s, nil, f.path, s.AdminBasic(), f.body)
		}
	})
}
