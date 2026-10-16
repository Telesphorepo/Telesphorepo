package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

// The Docker credential helper: the same binary under the name docker-credential-telesphorepo.
// Docker, buildx, Podman, Helm and ORAS run it through credHelpers in the Docker config file.

// dockerHelperName is the program name of the helper mode.
const dockerHelperName = "docker-credential-telesphorepo"

// Docker user names of the credentials that the helper gives.
const (
	deviceUser  = "telesphorepo-device"
	sessionUser = "telesphorepo"
	ciUser      = "telesphorepo-ci"
)

// Environment of a child of "publish" that pushes with a Docker client (a Helm chart): the
// session token, and the hosts that the helper answers it for.
const (
	envToken       = "TELESPHOREPO_TOKEN"
	envDockerHosts = "TELESPHOREPO_DOCKER_HOSTS"
)

// notFound is the answer that tells Docker the helper has no credential for a host. Docker then
// reads anonymously.
const notFound = "credentials not found in native keychain"

var errNotFound = errors.New(notFound)

const dockerOverview = `docker-credential-telesphorepo is the Docker credential helper of the Telesphorepo CLI.
Docker runs it; you do not. "telesphorepo setup" adds it to the Docker config file.

  echo repo.example.com | docker-credential-telesphorepo get

Commands:
  get | store | erase | list   Docker's credential helper protocol
  version                      print the version
`

var dockerHelps = map[string]string{
	"get": `Docker runs "get" with the registry host on stdin:

  echo repo.example.com | docker-credential-telesphorepo get

With a device of the server, the answer is a proof signed by the device key, which the registry
turns into a publish session. A process that runs "get" again gets the same proof while it is
valid (exchange.json next to the device file), so all token requests of one push use one
session. In a CI job with no device, the answer is the identity token of the job, for the server
in TELESPHOREPO_SERVER (the Docker CI files of the server set it). The token must name that
server as its audience. A host of no
server of this computer answers "` + notFound + `", so Docker reads anonymously.
`,
	"store": `The helper keeps no passwords, so "store" fails. Set up the computer instead:

  telesphorepo login https://repo.example.com
`,
	"erase": `Docker runs "erase" at docker logout. The helper keeps the device; to remove it, run:

  telesphorepo logout https://repo.example.com
`,
	"list": `List the registry hosts of the devices of this computer:

  echo unused | docker-credential-telesphorepo list
`,
}

// dockerMain runs the helper mode and returns the exit code.
func dockerMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, dockerOverview)
		return 2
	}
	var err error
	switch cmd := args[0]; cmd {
	case "get":
		err = dockerGet(os.Stdin, os.Stdout)
	case "store":
		err = errors.New("docker-credential-telesphorepo keeps no passwords. Run: telesphorepo login <server URL>")
	case "erase":
		fmt.Fprintln(os.Stderr, "docker-credential-telesphorepo keeps the device of this computer. To remove it, run: telesphorepo logout <server URL>")
	case "list":
		err = dockerList(os.Stdout)
	case "version", "--version":
		fmt.Println(dockerHelperName + " " + version)
	case "help", "--help", "-h":
		if len(args) > 1 && dockerHelps[args[1]] != "" {
			fmt.Print(dockerHelps[args[1]])
		} else {
			fmt.Print(dockerOverview)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, dockerOverview)
		return 2
	}
	if err != nil {
		// Docker reads the error of a helper from stdout.
		fmt.Fprintln(os.Stdout, err)
		return 1
	}
	return 0
}

// readServerURL reads the server URL that Docker writes on stdin.
func readServerURL(in io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(in, 4096))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", errors.New("no server URL on stdin")
	}
	return s, nil
}

// deviceFor returns the enrolled device that serves the registry host serverURL, and its folder.
// It checks the identity of the server: the host and port of serverURL must be those of the
// server, or a host of its hosts.json.
func deviceFor(serverURL string) (*deviceFile, string, error) {
	ds, dirs, err := enrolledDevices()
	if err != nil {
		return nil, "", err
	}
	for i, d := range ds {
		key, err := registryKey(serverURL, d.id.scheme)
		if err != nil {
			return nil, "", errNotFound
		}
		for _, h := range d.hosts(dirs[i]) {
			if h == key {
				return d, dirs[i], nil
			}
		}
	}
	return nil, "", errNotFound
}

// credential is the answer of "get".
type credential struct {
	ServerURL string
	Username  string
	Secret    string
}

// dockerGet answers Docker's "get", in this order: the session token of a running "publish" for
// one of its hosts; a proof of the device that serves the host; in a CI job, the identity token
// of the job, which the registry's token endpoint mints from.
func dockerGet(in io.Reader, out io.Writer) error {
	serverURL, err := readServerURL(in)
	if err != nil {
		return err
	}
	answer := func(user, secret string) error {
		return json.NewEncoder(out).Encode(credential{serverURL, user, secret})
	}
	if tok := os.Getenv(envToken); tok != "" && sessionHost(serverURL) {
		return answer(sessionUser, tok)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	d, dir, err := deviceFor(serverURL)
	if err == nil {
		secret, err := deviceSecret(ctx, d, dir)
		if err != nil {
			return err
		}
		return answer(deviceUser, secret)
	}
	if inCI() {
		srv, err := registryServer(ctx, serverURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return errNotFound
		}
		tok, err := ciToken(ctx, srv)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return errNotFound
		}
		if tok != "" {
			mask(os.Stderr, tok)
			return answer(ciUser, tok)
		}
	}
	return errNotFound
}

// sessionHost reports whether the registry host serverURL is one of TELESPHOREPO_DOCKER_HOSTS.
func sessionHost(serverURL string) bool {
	key, err := registryKey(serverURL, "https")
	if err != nil {
		return false
	}
	for _, h := range strings.Split(os.Getenv(envDockerHosts), ",") {
		if k, err := registryKey(h, "https"); err == nil && k == key {
			return true
		}
	}
	return false
}

// envServer names the server of a CI job that pushes with Docker. The Docker CI files of the
// server set it to the server's base URL.
const envServer = "TELESPHOREPO_SERVER"

// registryServer is the canonical URL of the server of a registry host in a CI job. The identity
// token must have it as its audience: GitHub Actions gets it as the audience, and the helper
// refuses an OIDC_TOKEN with another one. In this order:
//   - TELESPHOREPO_SERVER, when the host is the host of that server or one of its Docker hosts
//     (GET /docker/hosts); another host is an error;
//   - the server of the project file of the current folder, when its host is the host;
//   - https://<host>.
func registryServer(ctx context.Context, serverURL string) (string, error) {
	host := serverURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	host = strings.ToLower(host)
	if raw := os.Getenv(envServer); raw != "" {
		id, err := parseIdentity(raw)
		if err != nil {
			return "", fmt.Errorf("%s: %w", envServer, err)
		}
		if id.hostKey() == host {
			return id.base(), nil
		}
		hosts, err := newClient(id).registryHosts(ctx)
		if err != nil {
			return "", err
		}
		for _, h := range hosts {
			if strings.ToLower(h) == host {
				return id.base(), nil
			}
		}
		return "", fmt.Errorf("%s is not a registry host of %s (%s), so %s gives it no identity token", host, id.base(), envServer, dockerHelperName)
	}
	if p, err := findProject("."); err == nil && p != nil && p.id.hostKey() == host {
		return p.id.base(), nil
	}
	return "https://" + host, nil
}

// deviceSecret returns the token exchange proof of device d. The process that runs the helper
// gets the same token exchange nonce on each "get" while the nonce is valid (exchange.json), and
// the server checks the device on each "get". A device that the server does not accept answers
// errNotFound, with the reason on stderr.
func deviceSecret(ctx context.Context, d *deviceFile, dir string) (string, error) {
	c := newClient(d.id)
	cp := attach(c, true)
	defer cp.drift(ctx)
	if err := cp.check(ctx, "helper/docker"); err != nil {
		reportCompat(err)
		return "", err
	}
	parent := currentParent()
	keep := loadExchange(dir).nonceOf(parent, time.Now())
	nonce, expires, err := c.challenge(ctx, d, true, keep)
	if refused(err) {
		fmt.Fprintf(os.Stderr, "%s does not accept device %d (%s). Run: telesphorepo login %s\n",
			d.id.base(), d.DeviceID, detailOf(err), d.id.base())
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	if nonce != keep {
		// Without the file the next "get" of this process asks for a new nonce; the credential
		// still works.
		if err := saveExchange(dir, parent, nonce, expires); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	return exchangeSecret(d, nonce), nil
}

// dockerList answers Docker's "list": every host of every enrolled device, with the user name.
func dockerList(out io.Writer) error {
	ds, dirs, err := enrolledDevices()
	if err != nil {
		return err
	}
	all := map[string]string{}
	for i, d := range ds {
		for _, h := range dockerHosts(d.id, dirs[i]) {
			all[h] = deviceUser
		}
	}
	return json.NewEncoder(out).Encode(all)
}
