// Command telesphorepo is the single Telesphorepo binary (spec §4): serve (default), dev,
// user add|passwd|reset-2fa, version, help.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

// command is one CLI command. Sub-commands (user add) are nested.
type command struct {
	name    string
	summary string // one line for the overview
	usage   string // example-led help for "<cmd> --help"
	run     func(args []string) error
	subs    []*command
	hidden  bool // not listed in the overview
}

// usageError prints the command's usage after the message and exits 2.
type usageError struct {
	msg string
	cmd *command
}

func (e *usageError) Error() string { return e.msg }

// helpRequest asks main to print a command's usage and exit 0.
type helpRequest struct{ cmd *command }

func (helpRequest) Error() string { return "help requested" }

var commands []*command

func init() {
	userCmd := &command{
		name:    "user",
		summary: "add a user, reset a password or second factors",
		usage: `Manage users in the local database.

  telesphorepo user add alice            prompt for alice's password, create her
  telesphorepo user add ops --admin      create an admin
  telesphorepo user passwd alice         reset alice's password
  telesphorepo user reset-2fa alice      remove alice's second factors (lost phone and codes)

Passwords are prompted twice (hidden) on a terminal, or read as one line from a pipe:
  echo 's3cretpass' | telesphorepo user add ci-bot
`,
	}
	userCmd.subs = []*command{
		{name: "add", summary: "create a user", usage: `Create a user (password: prompted twice on a terminal, else one line from stdin; min 8 chars).

  telesphorepo user add alice
  telesphorepo user add ops --admin
  echo 's3cretpass' | telesphorepo user add ci-bot
`, run: userAdd},
		{name: "passwd", summary: "reset a user's password", usage: `Reset a user's password and sign out their sessions.

  telesphorepo user passwd alice
  echo 'n3wpassword' | telesphorepo user passwd alice
`, run: userPasswd},
		{name: "reset-2fa", summary: "remove a user's second factors", usage: `Remove every authenticator app, passkey and recovery code of a user who lost them all.

  telesphorepo user reset-2fa alice

The user then signs in with the password and adds a new second factor on the Security page;
until then the sign-in reaches only that page. Enrolled devices stay valid.
`, run: userReset2FA},
	}
	userCmd.run = func(args []string) error {
		if len(args) == 0 {
			return helpRequest{userCmd}
		}
		return &usageError{unknownMsg("user "+args[0], args[0], userCmd.subs), userCmd}
	}

	commands = []*command{
		{name: "serve", summary: "run the server (default)", usage: `Run the server. Settings are managed in the web UI (Settings) and stored in the
database; secrets are encrypted with a master key.

  telesphorepo                         same as "telesphorepo serve"
  TR_DATA_DIR=/srv/tr telesphorepo serve

Environment (all optional): TR_DATA_DIR (default ./data), TR_LISTEN (default :8080),
TR_MASTER_KEY (base64 32 bytes; default <data>/master.key, generated on first start).
Any setting can also be forced with TR_<SETTING>, e.g. TR_STORAGE=s3; it is then read-only
in the UI.

On first start an "admin" user is created and its random password is printed once, and the
repository signing key "default" is generated and stored encrypted in the database.
`, run: serveCmd},
		{name: "dev", summary: "zero-config local server with demo data", usage: `Run a zero-config local server for development.

  telesphorepo dev             first run: admin, demo users and about 175,000 demo files
  telesphorepo dev --empty     first run: admin and one empty repo per format

Data lives in ./.telesphorepo-dev, the server listens on http://localhost:8080 and cookies work
over plain HTTP. The first run creates the admin and the demo data before the server starts:
public repos maven (with Gradle plugin markers), debian and raw, the private Maven repo
internal, and five demo users with their roles. The data is the same on every fresh run; its
times spread over the last two years. The URL and the passwords are printed on every start.
TR_* env still overrides. Start over with a fresh instance by deleting ./.telesphorepo-dev.
`, run: devCmd},
		userCmd,
		{name: "dbcheck", hidden: true, usage: `Check the query plans of every query against a fresh database.

  telesphorepo dbcheck

Applies all migrations to a temporary database, runs EXPLAIN QUERY PLAN on every query and
exits 1 when one scans a table or uses a temp B-tree. Prints "ok: N queries" otherwise.
`, run: dbcheckCmd},
		{name: "seed", hidden: true, usage: `Bulk-insert synthetic data for the scale test.

  telesphorepo seed 100000
  TR_DATA_DIR=/srv/tr telesphorepo seed 1000000

Splits N rows evenly over 10 repos per format (seed-<format>-00 to seed-<format>-09) for every
format that supports seeding, then runs ANALYZE. A format adds its own rows, for example one
package per row. Stop the server first.
`, run: seedCmd},
		{name: "version", summary: "print the version", usage: "Print the version.\n\n  telesphorepo version\n", run: versionCmd},
		{name: "help", summary: "show help for a command", usage: "Show help.\n\n  telesphorepo help\n  telesphorepo help user add\n", run: helpCmd},
	}
}

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	var hr helpRequest
	if errors.As(err, &hr) {
		printUsage(os.Stdout, hr.cmd)
		return
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, "error:", ue.msg)
		if ue.cmd == nil {
			fmt.Fprintln(os.Stderr, `Run "telesphorepo --help" for the list of commands.`)
		} else {
			fmt.Fprintln(os.Stderr)
			printUsage(os.Stderr, ue.cmd)
		}
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func run(args []string) error {
	if len(args) == 0 {
		return serveCmd(nil)
	}
	switch args[0] {
	case "-h", "--help", "-help":
		return helpRequest{}
	case "-v", "--version":
		return versionCmd(nil)
	}
	cmd, rest, err := resolve(args)
	if err != nil {
		return err
	}
	if hasHelpFlag(rest) && cmd.name != "help" {
		return helpRequest{cmd}
	}
	return cmd.run(rest)
}

// resolve finds the (sub)command named by args and returns it with the remaining arguments.
func resolve(args []string) (*command, []string, error) {
	cmd := find(commands, args[0])
	if cmd == nil {
		return nil, nil, &usageError{unknownMsg(args[0], args[0], commands), nil}
	}
	args = args[1:]
	for len(cmd.subs) > 0 && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub := find(cmd.subs, args[0])
		if sub == nil {
			break // the parent's run reports it
		}
		cmd, args = sub, args[1:]
	}
	return cmd, args, nil
}

func find(list []*command, name string) *command {
	for _, c := range list {
		if c.name == name {
			return c
		}
	}
	return nil
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// unknownMsg builds `unknown command "x"` plus a "did you mean" hint from the candidates.
func unknownMsg(full, name string, candidates []*command) string {
	msg := fmt.Sprintf("unknown command %q", full)
	best, bestD := "", 3 // suggest only close matches
	for _, c := range candidates {
		d := levenshtein(name, c.name)
		if strings.HasPrefix(c.name, name) && len(name) >= 2 {
			d = 1
		}
		if d < bestD {
			best, bestD = c.name, d
		}
	}
	if best != "" {
		prefix := strings.TrimSuffix(full, name)
		msg += fmt.Sprintf(" - did you mean %q?", strings.TrimSpace(prefix+best))
	}
	return msg
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

const overview = `Telesphorepo - a small self-hosted artifact repository server.

Usage:
  telesphorepo [command]

Commands:
%s
Examples:
  telesphorepo dev                     try it locally (prints URL, user and password)
  telesphorepo                         run the server (data in ./data)
  telesphorepo user add alice          create a user (prompts for the password)

Run "telesphorepo <command> --help" for details.
`

func printUsage(w *os.File, cmd *command) {
	if cmd == nil {
		var b strings.Builder
		for _, c := range commands {
			if c.hidden {
				continue
			}
			fmt.Fprintf(&b, "  %-9s %s\n", c.name, c.summary)
		}
		fmt.Fprintf(w, overview, b.String())
		return
	}
	fmt.Fprint(w, cmd.usage)
}

func helpCmd(args []string) error {
	if len(args) == 0 {
		return helpRequest{}
	}
	cmd, rest, err := resolve(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 && len(cmd.subs) > 0 {
		return &usageError{unknownMsg(cmd.name+" "+rest[0], rest[0], cmd.subs), cmd}
	}
	return helpRequest{cmd}
}

func noArgs(name string, args []string) error {
	if len(args) > 0 {
		return &usageError{fmt.Sprintf("%s takes no arguments (got %q)", name, args[0]), find(commands, name)}
	}
	return nil
}

func versionCmd(args []string) error {
	if err := noArgs("version", args); err != nil {
		return err
	}
	v := version
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					v += " (" + s.Value[:7] + ")"
				}
			}
		}
	}
	fmt.Println("telesphorepo", v)
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func serveCmd(args []string) error {
	if err := noArgs("serve", args); err != nil {
		return err
	}
	boot, err := config.LoadBoot(nil)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return server.Run(ctx, boot)
}
