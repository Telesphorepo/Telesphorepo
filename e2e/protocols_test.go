//go:build e2e

package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

// selfSigned returns a PEM certificate and key for host.
func selfSigned(t *testing.T, host string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}))
}

// httpVersion asks url with curl and the flags from a container on TestNetwork and returns the
// HTTP version of the answer.
func httpVersion(t *testing.T, url string, flags ...string) string {
	t.Helper()
	code, out := RunClient(t, curlImage, ClientOpts{Cmd: []string{"curl -sS -o /dev/null -w 'version=%{http_version}' " + strings.Join(flags, " ") + " " + url}})
	if code != 0 {
		t.Fatalf("curl %s: exit %d:\n%s", url, code, out)
	}
	_, v, _ := strings.Cut(out, "version=")
	return strings.TrimSpace(v)
}

// TestServerProtocols checks the listener: HTTP/2 with prior knowledge on the plain listener,
// HTTPS with HTTP/2 when the TLS settings name a certificate, and the checksum headers of a
// download.
func TestServerProtocols(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})

	t.Run("h2c", func(t *testing.T) {
		if v := httpVersion(t, s.InternalURL+"/healthz", "--http2-prior-knowledge"); v != "2" {
			t.Fatalf("HTTP/2 with prior knowledge: version %q, want 2", v)
		}
		if v := httpVersion(t, s.InternalURL+"/healthz", "--http1.1"); v != "1.1" {
			t.Fatalf("HTTP/1.1: version %q, want 1.1", v)
		}
	})

	t.Run("tls", func(t *testing.T) {
		alias := RandName("tls")
		cert, key := selfSigned(t, alias)
		ts := StartServer(t, ServerOpts{Alias: alias, TLS: true,
			Env:   map[string]string{"TR_TLS_CERT_FILE": "/tls/cert.pem", "TR_TLS_KEY_FILE": "/tls/key.pem"},
			Files: []ContainerFile{{Path: "/tls/cert.pem", Content: cert}, {Path: "/tls/key.pem", Content: key}}})
		if v := httpVersion(t, ts.InternalURL+"/healthz", "-k", "--http2"); v != "2" {
			t.Fatalf("HTTPS: version %q, want 2", v)
		}
	})

	t.Run("checksum headers", func(t *testing.T) {
		repo := RandName("sums")
		s.CreateRepo(t, repo, "maven", false, nil)
		jar := rawRandom(t, 4096)
		path := "/maven/" + repo + "/com/acme/lib/1.0/lib-1.0.jar"
		resp, b := s.Do(t, s.NewRequest(t, http.MethodPut, path, bytes.NewReader(jar)), s.PublishAuth(t, s.AdminUser, repo))
		wantStatus(t, resp, b, http.StatusCreated)
		s1, s5, s256 := sha1.Sum(jar), md5.Sum(jar), sha256.Sum256(jar)
		want := map[string]string{"X-Checksum-SHA1": hex.EncodeToString(s1[:]), "X-Checksum-MD5": hex.EncodeToString(s5[:]),
			"X-Checksum-SHA256": hex.EncodeToString(s256[:])}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			resp, b := s.Do(t, s.NewRequest(t, method, path, nil), s.AdminBasic())
			wantStatus(t, resp, b, http.StatusOK)
			for h, v := range want {
				if got := resp.Header.Get(h); got != v {
					t.Fatalf("%s %s: %s %q, want %q", method, path, h, got, v)
				}
			}
		}
	})
}
