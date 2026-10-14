package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const minPasswordLen = 8

func userAdd(args []string) error {
	cmd := find(find(commands, "user").subs, "add")
	var name string
	admin := false
	for _, a := range args {
		switch {
		case a == "--admin" || a == "-admin":
			admin = true
		case strings.HasPrefix(a, "-"):
			return &usageError{fmt.Sprintf("unknown flag %q", a), cmd}
		case name == "":
			name = a
		default:
			return &usageError{fmt.Sprintf("unexpected argument %q", a), cmd}
		}
	}
	if name == "" {
		return &usageError{"missing user name", cmd}
	}
	if !repos.NameRE.MatchString(name) {
		return fmt.Errorf("invalid user name %q (lowercase letters, digits, '.', '_' or '-'; starting with a letter or digit)", name)
	}

	return withUsers(func(ctx context.Context, as *auth.Service, _ string) error {
		if _, err := as.GetUser(ctx, name); err == nil {
			return fmt.Errorf("user %q already exists (to reset the password: telesphorepo user passwd %s)", name, name)
		} else if !errors.Is(err, auth.ErrNotFound) {
			return err
		}
		pw, err := readPassword("Password for " + name)
		if err != nil {
			return err
		}
		if _, err := as.CreateUser(ctx, name, pw, admin); err != nil {
			if errors.Is(err, auth.ErrExists) {
				return fmt.Errorf("user %q already exists", name)
			}
			return fmt.Errorf("create user: %w", err)
		}
		kind := "user"
		if admin {
			kind = "admin user"
		}
		fmt.Printf("Created %s %s\n", kind, name)
		return nil
	})
}

func userPasswd(args []string) error {
	cmd := find(find(commands, "user").subs, "passwd")
	var name string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-"):
			return &usageError{fmt.Sprintf("unknown flag %q", a), cmd}
		case name == "":
			name = a
		default:
			return &usageError{fmt.Sprintf("unexpected argument %q", a), cmd}
		}
	}
	if name == "" {
		return &usageError{"missing user name", cmd}
	}

	return withUsers(func(ctx context.Context, as *auth.Service, dataDir string) error {
		if _, err := as.GetUser(ctx, name); errors.Is(err, auth.ErrNotFound) {
			return fmt.Errorf("no user %q (create it with: telesphorepo user add %s)", name, name)
		} else if err != nil {
			return err
		}
		pw, err := readPassword("New password for " + name)
		if err != nil {
			return err
		}
		if _, err := as.UpdateUser(ctx, name, &pw, nil, nil); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		// Keep the dev banner truthful: "telesphorepo dev" prints the admin password from this file.
		if name == devAdminUser {
			pf := config.JoinDataDir(dataDir, config.DevAdminPasswordFile)
			if _, err := os.Stat(pf); err == nil {
				if err := writePrivate(pf, pw+"\n"); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not update %s: %v\n", pf, err)
				}
			}
		}
		fmt.Printf("Password changed for %s (existing sessions were signed out)\n", name)
		return nil
	})
}

// withUsers opens the database in the CLI data dir and runs fn with an auth service.
func withUsers(fn func(ctx context.Context, as *auth.Service, dataDir string) error) error {
	dir, fromDev := config.CLIDataDir()
	cfg := &config.Config{DataDir: dir} // user commands need no settings beyond the data dir
	if fromDev {
		fmt.Fprintf(os.Stderr, "Using data dir %s (the \"telesphorepo dev\" instance; set TR_DATA_DIR to choose another)\n", cfg.DataDir)
	} else {
		fmt.Fprintf(os.Stderr, "Using data dir %s\n", cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg.DBFile())
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBFile(), err)
	}
	defer d.Close()
	return fn(ctx, auth.New(d, config.Static(cfg), repos.New(d)), cfg.DataDir)
}

// readPassword prompts twice without echo on a terminal, else reads one line from stdin.
func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("no password on stdin (pipe one line, e.g. echo 's3cretpass' | telesphorepo user add NAME)")
		}
		return pw, checkPassword(pw)
	}
	for attempt := 0; attempt < 3; attempt++ {
		pw, err := promptHidden(fd, prompt+": ")
		if err != nil {
			return "", err
		}
		if err := checkPassword(pw); err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		again, err := promptHidden(fd, "Repeat password: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			fmt.Fprintln(os.Stderr, "Passwords do not match, try again.")
			continue
		}
		return pw, nil
	}
	return "", errors.New("no password set")
}

func promptHidden(fd int, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}

func checkPassword(pw string) error {
	if len(pw) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	return nil
}

// writePrivate writes data to name with mode 0600 (also tightening an existing file's mode).
func writePrivate(name, data string) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_ = f.Chmod(0o600) // best effort; not meaningful on Windows
	if _, err := io.WriteString(f, data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
