//go:build e2e || clients

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	mobynet "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Keycloak test fixture (see testdata/keycloak/realm-e2e.json). The realm has two clients:
// "telesphorepo" for SSO sign-in, and "ci-github", a CI issuer for publish sessions
// whose client-credentials tokens carry GitHub-shaped claims.
const (
	keycloakImage      = "quay.io/keycloak/keycloak:26.8.1" // E2E_KEYCLOAK_IMAGE overrides
	kcRealm            = "e2e"
	kcClientID         = "telesphorepo"
	kcClientSecret     = "e2e-telesphorepo-client-secret"
	kcAdminGroup       = "repo-admins"
	kcAlicePassword    = "alice-e2e-password"
	kcBobPassword      = "bob-e2e-password"
	oidcCallbackPath   = "/api/auth/oidc/callback"
	oidcLoginPath      = "/api/auth/oidc/login"
	kcRedirectSentinel = "__REDIRECT_URI__"

	kcCIClientID       = "ci-github"
	kcCIClientSecret   = "e2e-ci-github-secret"
	kcAudienceSentinel = "__CI_AUDIENCE__"
	// The claims every ci-github token carries (hardcoded claim mappers in the realm).
	kcCIRepository  = "acme/app"
	kcCIRef         = "refs/tags/v1.0.0"
	kcCIWorkflowRef = "acme/app/.github/workflows/release.yml@refs/tags/v1.0.0"
	kcCISHA         = "0123456789abcdef0123456789abcdef01234567"
	kcCIRunID       = "8123456789"
	kcCIRunAttempt  = "2"
	kcCIActor       = "octocat"
	kcCIWorkflow    = "Release"
)

// Keycloak is a running Keycloak reachable under the same URL from the test process and from
// containers on TestNetwork: it listens inside its container on Port, is published on the same
// host port, and has the network alias Alias. Containers resolve Alias via the network; the test
// process maps Alias to 127.0.0.1 in its dialer (see Keycloak.Client).
type Keycloak struct {
	Alias  string
	Port   int
	Issuer string // http://<alias>:<port>/realms/e2e
	// CIAudience is the audience of ci-github tokens; set it before Start (usually the
	// server's base_url, the default audience of a trust policy).
	CIAudience string

	c *testcontainers.DockerContainer
}

// freeHostPort picks a free TCP port on the host in 20000-29999. The OS's own ephemeral ports
// (Windows 49152-65535) overlap the range the container engine uses for published ports
// (Linux 32768-60999); a port that is free on the host can then be bound already inside a
// Podman VM, and Keycloak, which listens on the same port inside its container, fails to start.
// The probe listens on loopback only: a listener on all interfaces makes the Windows firewall
// prompt for every new test binary.
func freeHostPort(t testing.TB) int {
	t.Helper()
	for range 200 {
		port := 20000 + rand.IntN(10000)
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		l.Close()
		return port
	}
	t.Fatal("find free port: no free port in 20000-29999")
	return 0
}

// newKeycloak picks the alias and the port so the issuer URL is known before anything starts.
func newKeycloak(t testing.TB) *Keycloak {
	alias := RandName("kc")
	port := freeHostPort(t)
	return &Keycloak{Alias: alias, Port: port, Issuer: fmt.Sprintf("http://%s:%d/realms/%s", alias, port, kcRealm)}
}

// Start runs Keycloak in start-dev mode with the e2e realm imported; redirectURI is the only
// redirect URI registered for the SSO client.
func (kc *Keycloak) Start(t testing.TB, redirectURI string) {
	t.Helper()
	realm, err := os.ReadFile("testdata/keycloak/realm-e2e.json")
	if err != nil {
		t.Fatalf("read realm: %v", err)
	}
	audience := kc.CIAudience
	if audience == "" {
		audience = "telesphorepo-e2e"
	}
	realmJSON := strings.NewReplacer(kcRedirectSentinel, redirectURI, kcAudienceSentinel, audience).Replace(string(realm))
	img := os.Getenv("E2E_KEYCLOAK_IMAGE")
	if img == "" {
		img = keycloakImage
	}
	portSpec := strconv.Itoa(kc.Port) + "/tcp"
	c, err := testcontainers.Run(context.Background(), img,
		testcontainers.WithCmd("start-dev", "--import-realm", "--http-port="+strconv.Itoa(kc.Port)),
		testcontainers.WithEnv(map[string]string{
			// Every URL Keycloak issues (issuer, endpoints, form actions) uses this hostname.
			"KC_HOSTNAME": fmt.Sprintf("http://%s:%d", kc.Alias, kc.Port),
		}),
		testcontainers.WithExposedPorts(portSpec),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PortBindings = mobynet.PortMap{
				mobynet.MustParsePort(portSpec): []mobynet.PortBinding{{HostPort: strconv.Itoa(kc.Port)}},
			}
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(realmJSON), ContainerFilePath: "/opt/keycloak/data/import/realm-e2e.json", FileMode: 0o644,
		}),
		network.WithNetwork([]string{kc.Alias}, TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/realms/"+kcRealm+"/.well-known/openid-configuration").
			WithPort(portSpec).WithStartupTimeout(3*time.Minute)),
	)
	registerCleanup(t, c, "keycloak "+kc.Alias)
	if err != nil {
		t.Fatalf("start keycloak: %v", err)
	}
	kc.c = c
}

// Stop terminates Keycloak, so that nothing can reach the issuer any more.
func (kc *Keycloak) Stop(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := kc.c.Terminate(ctx); err != nil {
		t.Fatalf("stop keycloak: %v", err)
	}
}

// Client returns a browser-like client: it keeps cookies (jar may be nil), follows redirects
// until the response of the server's OIDC callback (returned as is, so its Location can be
// inspected), and resolves the Keycloak alias to 127.0.0.1.
func (kc *Keycloak) Client(jar http.CookieJar) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, nw, addr string) (net.Conn, error) {
		if host, port, err := net.SplitHostPort(addr); err == nil && host == kc.Alias {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, nw, addr)
	}
	return &http.Client{
		Timeout:   2 * time.Minute,
		Jar:       jar,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if via[len(via)-1].URL.Path == oidcCallbackPath {
				return http.ErrUseLastResponse
			}
			if len(via) >= 20 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// CIToken returns a new identity token of the ci-github client (client-credentials grant). It
// is a JWT with iss = Issuer, aud containing CIAudience, a jti, and the GitHub-shaped claims
// repository, ref, workflow_ref, sha, run_id, run_attempt, actor and workflow.
func (kc *Keycloak) CIToken(t testing.TB) string {
	t.Helper()
	resp, err := kc.Client(nil).PostForm(kc.Issuer+"/protocol/openid-connect/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {kcCIClientID},
		"client_secret": {kcCIClientSecret},
	})
	if err != nil {
		t.Fatalf("keycloak token request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &tok) != nil || tok.AccessToken == "" {
		t.Fatalf("keycloak token request: status %d: %s", resp.StatusCode, b)
	}
	return tok.AccessToken
}

// JWKS returns the realm's JSON Web Key Set, as an admin would paste it into a trust policy.
func (kc *Keycloak) JWKS(t testing.TB) string {
	t.Helper()
	resp, err := kc.Client(nil).Get(kc.Issuer + "/protocol/openid-connect/certs")
	if err != nil {
		t.Fatalf("keycloak JWKS: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"keys"`) {
		t.Fatalf("keycloak JWKS: status %d: %s", resp.StatusCode, b)
	}
	return string(b)
}
