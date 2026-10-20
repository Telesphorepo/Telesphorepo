// Command telesphorepo is the Telesphorepo CLI. It sets up a project once from a line that the
// Publish tab of a repository gives ("telesphorepo setup LINK"), and then publishes it with the
// standard publish command of its ecosystem ("telesphorepo publish"), from a computer with a
// device key and from CI with the identity token of the job. Under the name
// docker-credential-telesphorepo it is the Docker credential helper.
//
// The CLI imports no server package. It talks to the server only through its HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

const overview = `telesphorepo publishes your project to a Telesphorepo server.

Set up a project once, with the line from the Publish tab of the repository:
  curl -fsSL https://repo.example.com/install.sh | sh -s -- setup 'https://repo.example.com/#/setup/trsu_...'

Then publish, in the project folder:
  telesphorepo publish
  telesphorepo publish dist/app.zip        (raw and Debian repositories)

Commands:
  setup LINK          set up the project of this folder from a setup line
  publish [FILE...]   publish the project of this folder, or upload FILEs
  login SERVER        approve this computer as a device of SERVER
  logout [SERVER]     revoke the device of this computer and remove its files
  status              show the devices of this computer and the project of this folder
  update [SERVER]     install the CLI of the server
  version             print the version
  help [command]      show the help of a command

Run telesphorepo help <command> for examples.
`

var helps = map[string]string{
	"setup": `Set up the project of this folder from the setup line of the Publish tab:

  telesphorepo setup 'https://repo.example.com/#/setup/trsu_...'
  telesphorepo setup 'https://repo.example.com/#/setup/trsu_...' --name "build box"

A setup line works once, for 10 minutes. The CLI enrolls this computer as a device of the server
(or uses the device it has), then writes telesphorepo.toml with the server and the repository.
Commit that file: teammates and CI publish to the same repository. For a Docker repository the
CLI writes no file; it offers to add its credential helper to the Docker config file instead. On
a terminal it then offers a CI file (GitHub Actions, Jenkinsfile or a Jenkins freestyle job).
The device name defaults to the host name of the computer.
`,
	"publish": `Publish the project of this folder:

  telesphorepo publish
  telesphorepo publish dist/app.zip build/app.tar.gz   (a raw repository)
  telesphorepo publish build/hello_1.0_amd64.deb       (a Debian repository)
  telesphorepo publish dist/app-1.0-py3-none-any.whl   (a PyPI repository, without a build)

The CLI finds telesphorepo.toml in this folder or a folder above it, mints a publish session for
its repository, and runs the publish command of the project with the token in the env:
  Maven    mvn deploy (or ./mvnw), with a temp settings file and the repository as the target
  Gradle   gradle publish (or ./gradlew), with an init script for projects with maven-publish
  Helm     helm package, then helm push to the registry
  raw      uploads each FILE into the folder "path" of telesphorepo.toml
  Debian   uploads each .deb to "dist" and "component" of telesphorepo.toml
  Python   poetry build ([tool.poetry]), uv build (uv.lock) or python -m build, then uploads
           each wheel and sdist
A Docker repository publishes with docker push and the credential helper.

In CI the CLI mints the session with the identity token of the job: GitHub Actions (the job
needs permissions: id-token: write) or OIDC_TOKEN (Jenkins). On a computer without a device of
the server, it first asks a person to approve the computer in the browser.
`,
	"login": `Approve this computer as a device of a server, with no project:

  telesphorepo login https://repo.example.com
  telesphorepo login https://repo.example.com --name "build box"

The CLI prints a link and a code. Open the link, sign in and approve the code. The CLI saves the
device key in ~/.telesphorepo/<server>/device.json (only you can read it). A computer that is a
device of the server already keeps its device.
`,
	"logout": `Revoke the device of this computer on a server and delete its files:

  telesphorepo logout
  telesphorepo logout https://repo.example.com

The CLI revokes the device on the server, removes the credential helper entries of the server
from the Docker config file, and deletes ~/.telesphorepo/<server>/. Without SERVER it acts on
the server of the project of this folder, else on the one server of this computer.
`,
	"status": `Show the devices of this computer and the project of this folder:

  telesphorepo status

For each server: the device, whether the server accepts it, and the credential helper entries.
For this folder: telesphorepo.toml and the command that telesphorepo publish runs. It prints no
secret.
`,
	"update": `Install the CLI of the server:

  telesphorepo update
  telesphorepo update https://repo.example.com

The server of the project of this folder comes first, then SERVER, then the one server of this
computer. The CLI downloads the CLI of the server, checks its SHA-256 and replaces itself. A CLI
that is newer than the server's stays: a newer CLI works with an older server.
`,
	"version": `Print the version:

  telesphorepo version
`,
}

func main() {
	removeOld()
	prog := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))
	if prog == dockerHelperName {
		os.Exit(dockerMain(os.Args[1:]))
	}
	os.Exit(run(os.Args[1:]))
}

// run runs the command of args and returns the exit code.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, overview)
		return 2
	}
	cmd, rest := args[0], args[1:]
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		if len(rest) > 0 && helps[rest[0]] != "" {
			fmt.Print(helps[rest[0]])
		} else {
			fmt.Print(overview)
		}
		return 0
	}
	if cmd == "version" || cmd == "--version" {
		fmt.Println("telesphorepo " + version)
		return 0
	}
	f := commands[cmd]
	if f == nil {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, overview)
		return 2
	}
	for _, a := range rest {
		if a == "--help" || a == "-h" {
			fmt.Print(helps[cmd])
			return 0
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer drift(ctx)
	err := f(ctx, rest)
	if err == nil {
		return 0
	}
	code, again := handle(ctx, cmd, err)
	if !again {
		return code
	}
	if err := f(ctx, rest); err != nil {
		code, _ = handle(ctx, "", err)
		return code
	}
	return 0
}

// commands are the commands of the overview.
var commands = map[string]func(context.Context, []string) error{
	"setup":   setup,
	"publish": publish,
	"login":   login,
	"logout":  logout,
	"status":  status,
	"update":  updateCmd,
}

// usageError is a command with wrong arguments.
type usageError struct{ cmd string }

func (e *usageError) Error() string { return "wrong arguments for " + e.cmd }

func usage(cmd string) error { return &usageError{cmd} }

// handle reports err and returns the exit code. again is true when a remedy ran and the command
// should run once more: the device flow of the remedy login, or an update of the CLI for an
// entry of the change log. cmd is "" when the command already ran again.
func handle(ctx context.Context, cmd string, err error) (code int, again bool) {
	var ue *usageError
	var ee *exitError
	var ce *compatError
	var pe *problemError
	switch {
	case errors.As(err, &ue):
		fmt.Fprint(os.Stderr, helps[ue.cmd])
		return 2, false
	case errors.As(err, &ee):
		if ee.name != "" {
			fmt.Fprintln(os.Stderr, "telesphorepo: "+ee.Error())
		}
		return ee.code, false
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "telesphorepo: stopped")
		return 130, false
	case errors.As(err, &ce):
		return compatStop(ctx, cmd, ce), false
	case errors.As(err, &pe):
		pe.render(os.Stderr)
		if cmd == "" || !interactive() || pe.p.Remedy == nil {
			return 1, false
		}
		switch pe.p.Remedy.Action {
		case remedyLogin:
			id, perr := parseIdentity(pe.server)
			if perr != nil {
				return 1, false
			}
			if _, err := loginDevice(ctx, newClient(id), deviceName()); err != nil {
				report(err)
				return 1, false
			}
			return 0, true
		case remedyUpdateCLI:
			id, perr := parseIdentity(pe.server)
			if perr == nil && confirm("Update now?", true) {
				if err := update(ctx, id); err != nil {
					report(err)
				}
			}
		}
		return 1, false
	}
	report(err)
	return 1, false
}

// report prints an error of the CLI on stderr.
func report(err error) {
	var pe *problemError
	if errors.As(err, &pe) {
		pe.render(os.Stderr)
		return
	}
	var ce *compatError
	if errors.As(err, &ce) {
		reportCompat(err)
		return
	}
	fmt.Fprintln(os.Stderr, "telesphorepo: "+err.Error())
}

// reportCompat prints a compatError: the summaries of the entries, then what the person does.
func reportCompat(err error) {
	var ce *compatError
	if !errors.As(err, &ce) {
		report(err)
		return
	}
	for _, s := range ce.summaries {
		fmt.Fprintln(os.Stderr, s)
	}
	id, _ := parseIdentity(ce.server)
	if ce.cli {
		fmt.Fprintf(os.Stderr, "This needs CLI %s; you have %s. Install the CLI of the server:\n  %s\n", ce.need, ce.running, installLine(id))
		return
	}
	fmt.Fprintf(os.Stderr, "This needs server %s; the server runs %s. Ask the server admin to update.\n", ce.need, ce.running)
}

// compatStop handles an entry of the change log that stops the command cmd. For an entry that
// needs a newer CLI, on a terminal, it offers the update and then runs the command again with the
// new binary. It returns the exit code.
func compatStop(ctx context.Context, cmd string, ce *compatError) int {
	if !ce.cli || cmd == "" || !interactive() {
		reportCompat(ce)
		return 1
	}
	for _, s := range ce.summaries {
		fmt.Fprintln(os.Stderr, s)
	}
	id, err := parseIdentity(ce.server)
	if err != nil || !confirm("Update now?", true) {
		fmt.Fprintf(os.Stderr, "Install the CLI of the server:\n  %s\n", installLine(id))
		return 1
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		report(err)
		return 1
	}
	exe = filepath.Join(filepath.Dir(exe), cliName())
	if err := update(ctx, id); err != nil {
		report(err)
		return 1
	}
	again := exec.Command(exe, os.Args[1:]...)
	again.Stdin, again.Stdout, again.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := again.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return max(ee.ExitCode(), 1)
		}
		report(err)
		return 1
	}
	return 0
}

// drift runs the drift notice for each server that the command talked to. After an update of
// the CLI it has nothing to say.
func drift(ctx context.Context) {
	if updated {
		return
	}
	seen := map[string]bool{}
	for _, cp := range compats {
		if !seen[cp.id.base()] {
			seen[cp.id.base()] = true
			cp.drift(ctx)
		}
	}
}
