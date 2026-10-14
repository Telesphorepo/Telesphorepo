package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
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
	settings       *settings.Service
}

func devCmd(args []string) error {
	if err := noArgs("dev", args); err != nil {
		return err
	}
	boot, err := config.LoadBoot(map[string]string{"TR_DATA_DIR": config.DevDataDir})
	if err != nil {
		return err
	}
	st := &devState{dataDir: boot.DataDir}
	ctx, stop := signalContext()
	defer stop()
	return server.RunWith(ctx, boot, server.Options{
		// Dev defaults (shown as defaults in the UI, still changeable there or via TR_* env), and
		// the second factor is optional ("2FA not enforced (dev)" in the banner).
		Dev:      true,
		TextLogs: true,
		Init:     func(ctx context.Context, s *server.Services) error { return devInit(ctx, s, st) },
		Ready:    func(addr net.Addr) { printDevBanner(st, baseURL(st.settings.Config(), addr)) },
	})
}

// devInit creates the admin (password saved to <data>/admin-password.txt) and the demo repos
// on first run, and loads the credentials for the banner on every run.
func devInit(ctx context.Context, s *server.Services, st *devState) error {
	cfg := s.Config
	st.settings = s.Settings
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
		pw := cfg.BootstrapAdminPassword
		if pw == "" {
			pw = server.RandomPassword()
		}
		if _, err := s.Auth.CreateUser(ctx, st.user, pw, true); err != nil {
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
	if os.Getenv("TELESPHOREPO_RUNNER") != "" {
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
	if st.firstRun {
		title += " (first run: admin and demo repos created)"
	}
	var b strings.Builder
	server.PrintBox(&b,
		title,
		"",
		"  url:      "+base,
		"  username: "+st.user,
		"  password: "+shownPW,
		"  data dir: "+st.dataDir,
		"",
		"2FA not enforced (dev)",
		"Start fresh by deleting the data dir.",
	)
	b.WriteString("Reads take the username and password above, or a read token.\n")
	b.WriteString("Uploads take a publish session: create one on the Publishing page.\n")
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
			fmt.Fprintf(&b, "  %s\n", sn.Title)
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
