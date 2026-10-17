//go:build e2e || clients

package e2e

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

// jenkinsIssuer plays the OpenID Connect Provider plugin of a Jenkins that the server cannot
// reach: it signs identity tokens with its own RSA key, and a trust policy holds its pasted keys.
// Its tokens carry the claims of the plugin: sub is the URL of the job, build_number the build.
type jenkinsIssuer struct {
	key *rsa.PrivateKey
	URL string // the Jenkins URL; the issuer is URL + "oidc"
}

const jenkinsKid = "e2e-jenkins"

func newJenkinsIssuer(t testing.TB) *jenkinsIssuer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &jenkinsIssuer{key: k, URL: "http://jenkins.example/"}
}

func (j *jenkinsIssuer) Issuer() string { return j.URL + "oidc" }

// JobURL is the URL of the job at path, e.g. "team/app" gives <URL>job/team/job/app/.
func (j *jenkinsIssuer) JobURL(job string) string {
	u := j.URL
	for _, part := range strings.Split(job, "/") {
		u += "job/" + part + "/"
	}
	return u
}

// JWKS is the key set that an admin pastes into the CI pipeline.
func (j *jenkinsIssuer) JWKS() string {
	enc := base64.RawURLEncoding.EncodeToString
	b, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": jenkinsKid,
		"n": enc(j.key.N.Bytes()), "e": enc(big.NewInt(int64(j.key.E)).Bytes()),
	}}})
	return string(b)
}

// Token is a fresh identity token of build n of job for the audience aud.
func (j *jenkinsIssuer) Token(t testing.TB, aud, job string, n int) string {
	t.Helper()
	now := time.Now().Unix()
	enc := base64.RawURLEncoding.EncodeToString
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": jenkinsKid})
	body, _ := json.Marshal(map[string]any{"iss": j.Issuer(), "aud": aud, "sub": j.JobURL(job),
		"build_number": n, "iat": now, "nbf": now, "exp": now + 600, "jti": RandName("jti")})
	signed := enc(head) + "." + enc(body)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, j.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + enc(sig)
}

// createJenkinsTrust adds an enabled CI pipeline for job of j on repos, with the pasted keys and
// the audience base_url (the policy's audience is empty).
func createJenkinsTrust(t testing.TB, s *Server, j *jenkinsIssuer, job string, repos ...string) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, "/api/publish/trusts", map[string]any{
		"name":     RandName("jenkins"),
		"issuer":   j.Issuer(),
		"audience": "",
		"claims":   map[string][]string{"sub": {j.JobURL(job)}},
		"jwks":     j.JWKS(),
		"repos":    repos,
	}, s.Admin(), nil)
}

// fileBuild is the build record of the file at p of repo, or nil.
type fileBuild struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Run        string `json:"run"`
}

func buildOf(t testing.TB, s *Server, repo, p string) *fileBuild {
	t.Helper()
	var f struct {
		Build *fileBuild `json:"build"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+p, nil, s.AdminBasic(), &f)
	return f.Build
}
