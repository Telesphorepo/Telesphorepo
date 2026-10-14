package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
)

// devAdminUser is the admin "telesphorepo dev" creates unless TR_BOOTSTRAP_ADMIN_USER is set.
const devAdminUser = "admin"

// devState is what Init learns and Ready prints.
type devState struct {
	user, password string
	dataDir        string
	mavenRepo      string
	debianRepo     string
	firstRun       bool
}

func devCmd(args []string) error {
	if err := noArgs("dev", args); err != nil {
		return err
	}
	cfg, err := config.LoadWithDefaults(map[string]string{
		"TR_DATA_DIR":      config.DevDataDir,
		"TR_COOKIE_SECURE": "false",
		"TR_DEB_DEBOUNCE":  "300ms",
	})
	if err != nil {
		return err
	}
	st := &devState{dataDir: cfg.DataDir, mavenRepo: "maven", debianRepo: "debian"}
	ctx, stop := signalContext()
	defer stop()
	return server.RunWith(ctx, cfg, server.Options{
		TextLogs: true,
		Init:     func(ctx context.Context, s *server.Services) error { return devInit(ctx, s, st) },
		Ready:    func(addr net.Addr) { printDevBanner(st, baseURL(cfg, addr)) },
	})
}

// devInit creates the admin (password saved to <data>/admin-password.txt) and the demo repos
// on first run, and loads the credentials for the banner on every run.
func devInit(ctx context.Context, s *server.Services, st *devState) error {
	cfg := s.Config
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
		for _, r := range []struct{ name, format string }{
			{"maven", repos.FormatMaven}, {"debian", repos.FormatDebian},
		} {
			// Anonymous read so mvn/gradle/apt can resolve without credentials locally.
			if _, err := s.Repos.Create(ctx, r.name, r.format, true, nil); err != nil && !errors.Is(err, repos.ErrExists) {
				return fmt.Errorf("create demo repo %s: %w", r.name, err)
			}
		}
		return nil
	}
	var mvn, deb string
	for _, r := range list {
		switch {
		case r.Format == repos.FormatMaven && (mvn == "" || r.Name == "maven"):
			mvn = r.Name
		case r.Format == repos.FormatDebian && (deb == "" || r.Name == "debian"):
			deb = r.Name
		}
	}
	if mvn != "" {
		st.mavenRepo = mvn
	}
	if deb != "" {
		st.debianRepo = deb
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
// Under the dev runner (bun run dev / preview set TELESPHOREPO_RUNNER=1) only the READY line is
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
		"Start fresh by deleting the data dir.",
	)
	mvnURL := base + "/maven/" + st.mavenRepo
	debURL := base + "/debian/" + st.debianRepo
	fmt.Fprintf(&b, `Maven  ~/.m2/settings.xml:
  <server><id>telesphorepo</id><username>%[1]s</username><password>%[2]s</password></server>
  repository/distributionManagement url: %[3]s

Gradle  (build.gradle.kts):
  maven {
      url = uri("%[3]s")
      isAllowInsecureProtocol = true
      credentials { username = "%[1]s"; password = "%[2]s" }
  }

apt:
  curl -fsSL %[4]s/keys/debian.asc | sudo tee /etc/apt/keyrings/telesphorepo.asc >/dev/null
  echo "deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] %[5]s stable main" | sudo tee /etc/apt/sources.list.d/telesphorepo.list

Upload with curl:
  curl -u %[1]s:%[2]s -T hello_1.0_amd64.deb %[5]s/upload/stable/main
  curl -u %[1]s:%[2]s -T app-1.0.jar %[3]s/com/example/app/1.0/app-1.0.jar

`, st.user, pw, mvnURL, base, debURL)
	b.WriteString(ready)
	fmt.Fprint(os.Stdout, b.String())
}
