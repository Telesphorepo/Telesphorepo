//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
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

// Keycloak test fixture (see testdata/keycloak/realm-e2e.json): the identity provider of single
// sign-on, with the client "telesphorepo".
var keycloakImage = floatingImage("quay.io/keycloak/keycloak:latest")

const (
	kcRealm            = "e2e"
	kcClientID         = "telesphorepo"
	kcClientSecret     = "e2e-telesphorepo-client-secret"
	kcCIClientID       = "e2e-ci" // the client-credentials client of a CI pipeline
	kcCIClientSecret   = "e2e-ci-client-secret"
	kcCIAudience       = "telesphorepo-e2e-ci" // the audience its tokens carry
	kcAdminGroup       = "repo-admins"
	kcAlicePassword    = "alice-e2e-password"
	kcBobPassword      = "bob-e2e-password"
	oidcCallbackPath   = "/api/auth/oidc/callback"
	oidcLoginPath      = "/api/auth/oidc/login"
	kcRedirectSentinel = "__REDIRECT_URI__"
)

// Keycloak is a running Keycloak reachable under the same URL from the test process and from
// containers on TestNetwork: it listens inside its container on Port, is published on the same
// host port, and has the network alias Alias. Containers resolve Alias via the network; the test
// process maps Alias to 127.0.0.1 in its dialer (see Keycloak.Client).
type Keycloak struct {
	Alias  string
	Port   int
	Issuer string // http://<alias>:<port>/realms/e2e

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
	realmJSON := strings.ReplaceAll(string(realm), kcRedirectSentinel, redirectURI)
	portSpec := strconv.Itoa(kc.Port) + "/tcp"
	c, err := runContainer(context.Background(), keycloakImage,
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
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute, wait.ForHTTP("/realms/"+kcRealm+"/.well-known/openid-configuration").
			WithPort(portSpec).WithStartupTimeout(3*time.Minute)),
	)
	registerCleanup(t, c, "keycloak "+kc.Alias)
	if err != nil {
		t.Fatalf("start keycloak: %v", err)
	}
	kc.c = c
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
