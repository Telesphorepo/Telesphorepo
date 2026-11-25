//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
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

// protoMajor GETs url/healthz through tr and returns the major HTTP version of the answer.
func protoMajor(t *testing.T, tr *http.Transport, url string) int {
	t.Helper()
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: time.Minute}).Get(url + "/healthz")
	if err != nil {
		t.Fatalf("GET %s/healthz: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s/healthz: status %d", url, resp.StatusCode)
	}
	return resp.ProtoMajor
}

// TestServerProtocols checks the listener: HTTP/2 with prior knowledge on the plain listener, and
// HTTPS with the configured certificate and HTTP/2 when the TLS settings name one.
func TestServerProtocols(t *testing.T) {
	t.Parallel()

	t.Run("h2c", func(t *testing.T) {
		t.Parallel()
		s := StartServer(t, ServerOpts{})
		var p http.Protocols
		p.SetUnencryptedHTTP2(true)
		if v := protoMajor(t, &http.Transport{Protocols: &p}, s.URL); v != 2 {
			t.Fatalf("HTTP/2 with prior knowledge: HTTP/%d, want 2", v)
		}
	})

	t.Run("tls", func(t *testing.T) {
		t.Parallel()
		alias := RandName("tls")
		cert, key := selfSigned(t, alias)
		ts := StartServer(t, ServerOpts{Alias: alias, TLS: true,
			Env:   map[string]string{"TR_TLS_CERT_FILE": "/tls/cert.pem", "TR_TLS_KEY_FILE": "/tls/key.pem"},
			Files: []ContainerFile{{Path: "/tls/cert.pem", Content: cert}, {Path: "/tls/key.pem", Content: key}}})
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(cert))
		var p http.Protocols
		p.SetHTTP2(true)
		tr := &http.Transport{Protocols: &p, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: alias}}
		if v := protoMajor(t, tr, ts.URL); v != 2 {
			t.Fatalf("HTTPS: HTTP/%d, want 2", v)
		}
	})
}
