package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// publish publishes the project of the current folder, or the FILE arguments to a raw or Debian
// repo: it finds telesphorepo.toml, mints one session for its repo, runs the row of the repo's
// format, and closes the session at exit, also on Ctrl-C.
func publish(ctx context.Context, files []string) error {
	for _, f := range files {
		if strings.HasPrefix(f, "-") && f != "-" {
			return usage("publish")
		}
	}
	p, err := findProject(".")
	if err != nil {
		return err
	}
	if p == nil {
		return noProject()
	}
	c := newClient(p.id)
	cp := attach(c, false)
	if err := cp.check(ctx, "publish"); err != nil {
		return err
	}
	s, err := mint(ctx, c, p.Repo)
	if err != nil {
		return err
	}
	defer func() {
		if err := c.closeSession(s); err != nil {
			fmt.Fprintf(os.Stderr, "Cannot close publish session %d (%v); it ends at %s.\n", s.ID, err, s.ExpiresAt.Local().Format(time.TimeOnly))
		}
	}()
	mask(os.Stdout, s.Token)
	fmt.Fprintf(os.Stderr, "Publishing to %s (%s) on %s, session %d.\n", s.Repo, s.Format, p.id.base(), s.ID)
	if err := cp.check(ctx, "publish/"+s.Format); err != nil {
		return err
	}

	r, ok, err := pickRow(s.Format, p.Kind, p.dir)
	if err != nil {
		return err
	}
	if !ok {
		if h, isHelper := helperFormats[s.Format]; isHelper {
			return helperStop(c, h, p.Repo)
		}
		if len(rowsOf(s.Format)) == 0 {
			return fmt.Errorf("this CLI cannot publish to %s repositories. Run: telesphorepo update %s", s.Format, p.id.base())
		}
		return fmt.Errorf("%s has no project of a %s repository: telesphorepo publish looks for %s", p.dir, s.Format, markersOf(s.Format))
	}
	if !r.takesFiles() && len(files) > 0 {
		return fmt.Errorf("telesphorepo publish runs the %s build of %s and takes no FILE arguments; they are for raw and Debian repositories", r.title, p.dir)
	}

	tmp, err := os.MkdirTemp("", "telesphorepo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := restrictDir(tmp); err != nil {
		return err
	}
	j := &job{ctx: ctx, c: c, p: p, s: s, tmp: tmp, files: files}
	err = r.run(j)
	var ee *exitError
	if errors.As(err, &ee) && time.Now().After(s.ExpiresAt) {
		fmt.Fprintf(os.Stderr, "Publish session %d ended at %s (the server setting publish.session_ttl), before %s did. Uploads after that time fail.\n",
			s.ID, s.ExpiresAt.Local().Format(time.TimeOnly), ee.name)
	}
	return err
}

// noProject is the error of a folder without telesphorepo.toml: on a computer it names the
// Publish tab, in CI it says to commit the file.
func noProject() error {
	if inCI() {
		return fmt.Errorf("no %s in this folder or above. Commit the %s that telesphorepo setup wrote", projectFileName, projectFileName)
	}
	where := "of the repository on the server"
	if ds, _, err := enrolledDevices(); err == nil && len(ds) == 1 {
		where = "of the repository on " + ds[0].Server
	}
	return fmt.Errorf("no %s in this folder or above. Copy the setup line from the Publish tab %s, and run it here", projectFileName, where)
}

// mint mints a session for repo: in CI with the identity token of the job, on a computer with
// the device proof. A computer without a device that the server accepts runs the device flow
// first.
func mint(ctx context.Context, c *client, repo string) (*session, error) {
	if inCI() {
		tok, err := ciToken(ctx, c.id.base())
		if err != nil {
			return nil, err
		}
		if tok != "" {
			mask(os.Stdout, tok)
			return c.mintCI(ctx, tok, repo)
		}
		if d, _, err := deviceOf(c.id); err != nil || d == nil {
			return nil, errors.New("this CI job has no identity token. In GitHub Actions, give the job permissions: id-token: write. " +
				"In Jenkins, bind the identity token credential to OIDC_TOKEN")
		}
	}
	d, err := ensureDevice(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.mintDevice(ctx, d, repo)
}

// helperStop is the end of "publish" for a repo of the helper category: the native command
// publishes, and the credential helper gives it the token.
func helperStop(c *client, h helperFormat, repo string) error {
	dir, _ := serverDir(c.id)
	hosts := dockerHosts(c.id, dir)
	cfg, _ := dockerConfigPath()
	fmt.Fprintf(os.Stderr, "%s publishes to %s with its own command:\n  %s\n", h.tool, repo, h.command(hosts[0], repo))
	if currentCredHelpers()[hosts[0]] == helperName {
		fmt.Fprintf(os.Stderr, "The credential helper of the CLI in %s gives it the token.\n", cfg)
	} else {
		fmt.Fprintf(os.Stderr, "Add the credential helper of the CLI to %s first (the setup line does it):\n  %s\n", cfg, credHelpersLine(hosts))
	}
	return &exitError{code: 1}
}

// credHelpersLine is the credHelpers entry of hosts in the Docker config file.
func credHelpersLine(hosts []string) string {
	parts := make([]string, len(hosts))
	for i, h := range hosts {
		parts[i] = fmt.Sprintf("%q: %q", h, helperName)
	}
	return `"credHelpers": {` + strings.Join(parts, ", ") + "}"
}
