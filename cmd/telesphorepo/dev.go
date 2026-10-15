package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// devAdminUser is the admin "telesphorepo dev" creates unless TR_BOOTSTRAP_ADMIN_USER is set.
const devAdminUser = "admin"

// devState is what Init learns and Ready prints.
type devState struct {
	user, password string
	dataDir        string
	repos          []*repos.Repo
	firstRun       bool
	empty          bool        // --empty: no demo data on the first run
	demoUsers      []*demoUser // the demo users, from the demo users file
	settings       *settings.Service
}

func devCmd(args []string) error {
	empty := false
	for _, a := range args {
		if a != "--empty" {
			return &usageError{fmt.Sprintf("unexpected argument %q", a), find(commands, "dev")}
		}
		empty = true
	}
	boot, err := config.LoadBoot(map[string]string{"TR_DATA_DIR": config.DevDataDir})
	if err != nil {
		return err
	}
	st := &devState{dataDir: boot.DataDir, empty: empty}
	ctx, stop := signalContext()
	defer stop()
	err = runDev(ctx, boot, st)
	// The dev data dir is throwaway: start over when a schema file changed since it was created
	// or when the demo data of the first run was not finished.
	var why string
	switch {
	case errors.Is(err, db.ErrSchemaChanged):
		why = fmt.Sprintf("the schema changed since %s was created (%v)", boot.DataDir, err)
	case errors.Is(err, errDemoIncomplete):
		why = errDemoIncomplete.Error()
	}
	if why != "" {
		fmt.Fprintf(os.Stderr, "dev: %s; starting with a fresh data dir\n", why)
		if rmErr := os.RemoveAll(boot.DataDir); rmErr != nil {
			return fmt.Errorf("reset dev data dir: %w", rmErr)
		}
		*st = devState{dataDir: boot.DataDir, empty: empty}
		err = runDev(ctx, boot, st)
	}
	return err
}

func runDev(ctx context.Context, boot *config.Boot, st *devState) error {
	return server.RunWith(ctx, boot, server.Options{
		// Dev defaults (shown as defaults in the UI, still changeable there or via TR_* env), and
		// the second factor is optional ("2FA not enforced (dev)" in the banner).
		Dev:      true,
		TextLogs: true,
		Init:     func(ctx context.Context, s *server.Services) error { return devInit(ctx, s, st) },
		Ready:    func(addr net.Addr) { printDevBanner(st, baseURL(st.settings.Config(), addr)) },
	})
}

// devInit creates the admin (password saved to <data>/admin-password.txt) on the first run,
// with the demo data (users, repos and files; see createDemoData) unless --empty, else one
// empty demo repo per format. It loads the credentials for the banner on every run.
func devInit(ctx context.Context, s *server.Services, st *devState) error {
	cfg := s.Config
	st.settings = s.Settings
	if _, err := os.Stat(config.JoinDataDir(cfg.DataDir, demoPendingFile)); err == nil {
		return errDemoIncomplete
	}
	pwFile := config.JoinDataDir(cfg.DataDir, config.DevAdminPasswordFile)
	st.user = devAdminUser
	if cfg.BootstrapAdminUser != "" {
		st.user = cfg.BootstrapAdminUser
	}

	has, err := s.Auth.HasUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if !has {
		if !st.empty {
			// createDemoData removes the marker when the demo data is complete.
			if err := writePrivate(config.JoinDataDir(cfg.DataDir, demoPendingFile), "the demo data is being written\n"); err != nil {
				return err
			}
		}
		pw := cfg.BootstrapAdminPassword
		if pw == "" {
			pw = server.RandomPassword()
		}
		if _, err := s.Auth.CreateUser(ctx, st.user, pw, auth.AllServerPerms); err != nil {
			return fmt.Errorf("create dev admin: %w", err)
		}
		if err := writePrivate(pwFile, pw+"\n"); err != nil {
			return fmt.Errorf("save dev admin password: %w", err)
		}
		st.password, st.firstRun = pw, true
	} else if b, err := os.ReadFile(pwFile); err == nil {
		st.password = strings.TrimSpace(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", pwFile, err)
	}

	list, err := s.Repos.List(ctx)
	if err != nil {
		return fmt.Errorf("list repos: %w", err)
	}
	if len(list) == 0 {
		if st.firstRun && !st.empty {
			if err := createDemoData(ctx, s, st.user); err != nil {
				return err
			}
		}
		for _, f := range format.All() {
			// Anonymous read so package clients can resolve without credentials locally.
			if _, err := s.Repos.Create(ctx, f.Name(), f.Name(), true, "Demo "+f.Title()+" repository", nil); err != nil && !errors.Is(err, repos.ErrExists) {
				return fmt.Errorf("create demo repo %s: %w", f.Name(), err)
			}
		}
		if list, err = s.Repos.List(ctx); err != nil {
			return fmt.Errorf("list repos: %w", err)
		}
	}
	st.repos = list
	if st.demoUsers, err = loadDemoUsers(config.JoinDataDir(cfg.DataDir, demoUsersFile)); err != nil {
		return fmt.Errorf("read demo users: %w", err)
	}
	return nil
}

// baseURL is TR_BASE_URL, or http://localhost:<port> of the bound listener.
func baseURL(cfg *config.Config, addr net.Addr) string {
	if cfg.BaseURL != "" {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	port := "8080"
	if ta, ok := addr.(*net.TCPAddr); ok {
		port = fmt.Sprint(ta.Port)
	}
	return "http://localhost:" + port
}

// printDevBanner prints credentials and copy-paste client snippets to stdout. Its last line is
// the stable machine-readable "TELESPHOREPO_READY url=... user=... password=..." (the dev tool
// parses it); password= is empty when the password is unknown.
//
// Under the dev runner (go tool task dev / preview set TELESPHOREPO_RUNNER=1) only the READY line is
// printed: the runner shows one combined banner, and a rebuild must not repeat the box.
func printDevBanner(st *devState, base string) {
	ready := fmt.Sprintf("TELESPHOREPO_READY url=%s user=%s password=%s data=%q\n", base, st.user, st.password, st.dataDir)
	var users []string
	for _, u := range st.demoUsers {
		users = append(users, fmt.Sprintf("  %-9s %-28s %s", u.name, u.role, u.password))
	}
	if os.Getenv("TELESPHOREPO_RUNNER") != "" {
		if st.firstRun && len(users) > 0 {
			fmt.Fprintf(os.Stdout, "Demo users (also in %s):\n%s\n", config.JoinDataDir(st.dataDir, demoUsersFile), strings.Join(users, "\n"))
		}
		fmt.Fprint(os.Stdout, ready)
		return
	}
	pw := st.password
	shownPW := pw
	if pw == "" {
		shownPW = "(unknown - reset it: telesphorepo user passwd " + st.user + ")"
		pw = "<password>"
	}
	title := "Telesphorepo dev server is running"
	switch {
	case st.firstRun && len(users) > 0:
		title += " (first run: admin, demo users and demo data created)"
	case st.firstRun:
		title += " (first run: admin and demo repos created)"
	}
	lines := []string{
		title,
		"",
		"  url:      " + base,
		"  username: " + st.user,
		"  password: " + shownPW,
		"  data dir: " + st.dataDir,
	}
	if len(users) > 0 {
		lines = append(lines, "", "Demo users (user, role, password):")
		lines = append(lines, users...)
	}
	lines = append(lines, "", "2FA not enforced (dev)", "Start fresh by deleting the data dir.")
	var b strings.Builder
	server.PrintBox(&b, lines...)
	b.WriteString("Reads take the username and password above, or a read token.\n")
	b.WriteString("Uploads take a publish session: see \"Publish to this repository\" on the repo page.\n")
	for _, f := range format.All() {
		d, ok := f.(format.Describer)
		if !ok {
			continue
		}
		r := demoRepo(st.repos, f.Name())
		if r == nil {
			continue
		}
		fmt.Fprintf(&b, "\n%s (repo %q):\n", f.Title(), r.Name)
		for _, sn := range d.Usage(r, base) {
			if sn.Purpose != format.SnippetUse {
				continue
			}
			fmt.Fprintf(&b, "  %s: %s\n", sn.Client, sn.Title)
			for _, line := range strings.Split(strings.TrimRight(sn.Code, "\n"), "\n") {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(ready)
	fmt.Fprint(os.Stdout, b.String())
}

// demoRepo picks the repo of the given format to show snippets for: the one named after the
// format, else the first of that format.
func demoRepo(list []*repos.Repo, formatName string) *repos.Repo {
	var first *repos.Repo
	for _, r := range list {
		if r.Format != formatName {
			continue
		}
		if r.Name == formatName {
			return r
		}
		if first == nil {
			first = r
		}
	}
	return first
}
