// Command telesphorepo: serve | user create | signing-key generate (spec §4).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
	"telesphorepo/internal/db"
	"telesphorepo/internal/repos"
	"telesphorepo/internal/server"
	"telesphorepo/internal/signing"
)

const usage = `usage:
  telesphorepo [serve]
  telesphorepo user create --username U [--admin] (--password-stdin | --password-env VAR)
  telesphorepo signing-key generate --name N --email E --out FILE [--public-out FILE]
`

// errUsage marks errors that should print usage and exit 2.
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	var ue errUsage
	if errors.As(err, &ue) || errors.Is(err, flag.ErrHelp) {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "serve" {
		if len(args) > 1 {
			return errUsage{"serve takes no arguments"}
		}
		return serve()
	}
	sub := strings.Join(args[:min(2, len(args))], " ")
	switch sub {
	case "user create":
		return userCreate(args[2:])
	case "signing-key generate":
		return keyGenerate(args[2:])
	case "help", "-h", "--help":
		return flag.ErrHelp
	}
	return errUsage{fmt.Sprintf("unknown command %q", sub)}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage{err.Error()}
	}
	if fs.NArg() > 0 {
		return errUsage{fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, cfg)
}

func userCreate(args []string) error {
	fs := newFlags("user create")
	username := fs.String("username", "", "")
	admin := fs.Bool("admin", false, "")
	pwStdin := fs.Bool("password-stdin", false, "")
	pwEnv := fs.String("password-env", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *username == "" {
		return errUsage{"--username is required"}
	}
	if *pwStdin == (*pwEnv != "") {
		return errUsage{"exactly one of --password-stdin or --password-env is required"}
	}

	var password string
	if *pwStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		v, ok := os.LookupEnv(*pwEnv)
		if !ok {
			return fmt.Errorf("environment variable %s is not set", *pwEnv)
		}
		password = v
	}
	if password == "" {
		return errors.New("password is empty")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	d, err := db.Open(ctx, cfg.DBFile())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer d.Close()
	u, err := auth.New(d, cfg, repos.New(d)).CreateUser(ctx, *username, password, *admin)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	fmt.Println(u.Username)
	return nil
}

func keyGenerate(args []string) error {
	fs := newFlags("signing-key generate")
	name := fs.String("name", "", "")
	email := fs.String("email", "", "")
	out := fs.String("out", "", "")
	pubOut := fs.String("public-out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" || *email == "" || *out == "" {
		return errUsage{"--name, --email and --out are required"}
	}
	priv, pub, err := signing.Generate(*name, *email)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	if err := writeNew(*out, priv, 0o600); err != nil {
		return err
	}
	if *pubOut != "" {
		if err := writeNew(*pubOut, pub, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeNew writes data to name with exactly perm (WriteFile keeps an existing file's mode).
func writeNew(name string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
