// Command docker-credential-telesphorepo is the Docker credential helper of Telesphorepo. It
// enrolls this computer as a device of a Telesphorepo server (a person approves it in the
// browser), keeps the device key in ~/.telesphorepo/<server>/docker/device.json, and answers
// Docker's "get" with a proof signed by the device key. The registry's token endpoint turns the
// proof into a publish session, so docker, buildx, podman, helm and oras push without a password.
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

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

// username is the user name of every credential that the helper gives Docker.
const username = "telesphorepo-device"

// notFound is the answer that tells Docker the helper has no credential for a host. Docker then
// reads anonymously.
const notFound = "credentials not found in native keychain"

const overview = `docker-credential-telesphorepo signs Docker pushes to a Telesphorepo server with a device key.

Enroll this computer once (a person approves it in the browser):
  docker-credential-telesphorepo login https://repo.example.com

Then push and pull with no docker login:
  docker push repo.example.com/apps/api:1.0
  helm push chart-1.0.tgz oci://repo.example.com/charts

Commands:
  login <server URL> [--name NAME]   enroll this computer and set up Docker
  get | store | erase | list         Docker's credential helper protocol (Docker runs these)
  version                            print the version
  help [command]                     show help

Run docker-credential-telesphorepo help <command> for examples.
`

var helps = map[string]string{
	"login": `Enroll this computer as a device of a Telesphorepo server and set up Docker.

  docker-credential-telesphorepo login https://repo.example.com
  docker-credential-telesphorepo login https://repo.example.com --name "build box"

The helper prints a link and a code. Open the link, sign in and approve the code. The helper
then saves the device key in ~/.telesphorepo/<server>/docker/device.json (only you can read it)
and adds "credHelpers" for the registry hosts of the server to ~/.docker/config.json
($DOCKER_CONFIG/config.json when DOCKER_CONFIG is set). The device name defaults to
"<hostname> (Docker)". A computer that is already a device of the server keeps its device.
`,
	"get": `Docker runs "get" with the registry host on stdin, e.g.:

  echo repo.example.com | docker-credential-telesphorepo get

The answer is a proof signed by the device key, which the registry turns into a publish session.
A process that runs "get" again gets the same proof while it is valid (exchange.json next to the
device file), so all token requests of one push use one publish session. A host that no device of this computer serves answers "` + notFound + `", so Docker
reads anonymously.
`,
	"store": `The helper keeps no passwords, so "store" fails. Enroll the computer instead:

  docker-credential-telesphorepo login https://repo.example.com
`,
	"erase": `Docker runs "erase" at docker logout. It deletes the device key of the host from this computer:

  docker logout repo.example.com
  echo repo.example.com | docker-credential-telesphorepo erase

The device stays in Settings > Devices of the server until someone revokes it there.
`,
	"list": `List the registry hosts that the enrolled devices of this computer serve:

  echo unused | docker-credential-telesphorepo list
`,
	"version": `Print the version:

  docker-credential-telesphorepo version
`,
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, overview)
		os.Exit(2)
	}
	var err error
	switch cmd := args[0]; cmd {
	case "get":
		err = get(os.Stdin, os.Stdout)
	case "store":
		err = fmt.Errorf("docker-credential-telesphorepo keeps no passwords. Run: docker-credential-telesphorepo login <server URL>")
	case "erase":
		err = erase(os.Stdin)
	case "list":
		err = list(os.Stdout)
	case "version", "--version":
		fmt.Println("docker-credential-telesphorepo " + version)
	case "login":
		err = login(args[1:])
	case "help", "--help", "-h":
		if len(args) > 1 && helps[args[1]] != "" {
			fmt.Print(helps[args[1]])
		} else {
			fmt.Print(overview)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, overview)
		os.Exit(2)
	}
	if err != nil {
		// Docker reads the error of a helper from stdout.
		fmt.Fprintln(os.Stdout, err)
		os.Exit(1)
	}
}

// errNotFound is the error of a host without a credential.
var errNotFound = errors.New(notFound)

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
// server, or a host of its hosts.json. A device file that is not valid is skipped; the reason
// goes to stderr.
func deviceFor(serverURL string) (*deviceFile, string, error) {
	dirs, err := enrolledDirs()
	if err != nil {
		return nil, "", err
	}
	for _, dir := range dirs {
		d, err := loadDevice(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if d == nil {
			continue
		}
		key, err := registryKey(serverURL, d.id.scheme)
		if err != nil {
			return nil, "", errNotFound
		}
		for _, h := range d.hosts(dir) {
			if h == key {
				return d, dir, nil
			}
		}
	}
	return nil, "", errNotFound
}

// get answers Docker's "get": a credential of the device that serves the host on stdin. The
// process that runs the helper gets the same token exchange nonce on each "get" while the nonce
// is valid (exchange.json), and the server checks the device on each "get".
func get(in io.Reader, out io.Writer) error {
	serverURL, err := readServerURL(in)
	if err != nil {
		return err
	}
	d, dir, err := deviceFor(serverURL)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	parent := currentParent()
	keep := loadExchange(dir).nonceOf(parent, time.Now())
	nonce, expires, err := newClient(d.id).challenge(ctx, d, true, keep)
	var ae *apiError
	if errors.As(err, &ae) && ae.status >= 400 && ae.status < 500 {
		fmt.Fprintf(os.Stderr, "%s does not accept device %d (%s). Run: docker-credential-telesphorepo login %s\n",
			d.id.base(), d.DeviceID, ae.msg, d.id.base())
		return errNotFound
	}
	if err != nil {
		return err
	}
	if nonce != keep {
		// Without the file the next "get" of this process asks for a new nonce; the credential
		// still works.
		if err := saveExchange(dir, parent, nonce, expires); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	return json.NewEncoder(out).Encode(struct {
		ServerURL string
		Username  string
		Secret    string
	}{serverURL, username, exchangeSecret(d, nonce)})
}

// erase answers Docker's "erase": it deletes the device folder of the host on stdin.
func erase(in io.Reader) error {
	serverURL, err := readServerURL(in)
	if err != nil {
		return err
	}
	d, dir, err := deviceFor(serverURL)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cannot delete %s: %w", dir, err)
	}
	fmt.Fprintf(os.Stderr, "Deleted the key of device %d from this computer. The device stays in Settings > Devices of %s until someone revokes it there.\n",
		d.DeviceID, d.id.base())
	return nil
}

// list answers Docker's "list": every host of every enrolled device, with the user name.
func list(out io.Writer) error {
	dirs, err := enrolledDirs()
	if err != nil {
		return err
	}
	all := map[string]string{}
	for _, dir := range dirs {
		d, err := loadDevice(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if d == nil {
			continue
		}
		for _, h := range d.dockerHosts(dir) {
			all[h] = username
		}
	}
	return json.NewEncoder(out).Encode(all)
}

// login enrolls this computer as a device of the server (or keeps the device it has), saves the
// device file and hosts.json, and adds the hosts to the credHelpers of Docker.
func login(args []string) error {
	var server, name string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--name" && i+1 < len(args):
			name = args[i+1]
			i++
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
		case a == "--help" || a == "-h":
			fmt.Print(helps["login"])
			return nil
		case strings.HasPrefix(a, "-") || server != "":
			fmt.Fprint(os.Stderr, helps["login"])
			os.Exit(2)
		default:
			server = a
		}
	}
	if server == "" {
		fmt.Fprint(os.Stderr, helps["login"])
		os.Exit(2)
	}
	id, err := parseIdentity(server)
	if err != nil {
		return err
	}
	if name == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		name = host + " (Docker)"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := newClient(id)
	hosts, err := c.registryHosts(ctx)
	if err != nil {
		return fmt.Errorf("%s is not a Telesphorepo server with Docker repositories: %w", id.base(), err)
	}
	dir, err := deviceDirOf(id)
	if err != nil {
		return err
	}
	d, err := loadDevice(dir)
	if err != nil {
		return err
	}
	if d != nil {
		_, _, err := c.challenge(ctx, d, false, "")
		var ae *apiError
		switch {
		case err == nil:
			fmt.Printf("This computer is already device %d of %s.\n", d.DeviceID, id.base())
		case errors.As(err, &ae) && ae.status >= 400 && ae.status < 500:
			fmt.Printf("%s no longer accepts device %d (%s).\n", id.base(), d.DeviceID, ae.msg)
			d = nil
		default:
			return err
		}
	}
	if d == nil {
		if d, err = c.enroll(ctx, name); err != nil {
			return err
		}
	}
	f, err := d.save(dir, hosts)
	if err != nil {
		return err
	}
	fmt.Printf("This computer is device %d. Its key is in %s\n", d.DeviceID, f)
	cfg, err := addCredHelpers(hosts)
	if err != nil {
		return err
	}
	fmt.Printf("Docker now asks the helper for %s (%s).\n", strings.Join(hosts, " and "), cfg)
	fmt.Printf("Push with: docker push %s/<repository>/<image>:<tag>\n", hosts[0])
	if len(hosts) > 1 {
		fmt.Printf("Push with ORAS and Helm 4 to the upload host: helm push <chart> oci://%s/<repository>\n", hosts[1])
	}
	return nil
}
