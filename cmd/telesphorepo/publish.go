package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// publish publishes the project of the current folder, or the FILE arguments to a raw, Debian, Arch or RPM
// repo: it finds telesphorepo.toml, mints one session for its repo, runs the row of the repo's
// format, and closes the session at exit, also on Ctrl-C.
func publish(ctx context.Context, args []string) error {
	put, files, err := parsePutFlags(args)
	if err != nil {
		return err
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
	s, err := mint(ctx, c, p.Repo, p.dir)
	if err != nil {
		return err
	}
	defer func() {
		if err := c.closeSession(s); err != nil {
			warn("Cannot close publish session %d (%v); it ends at %s.", s.ID, err, s.ExpiresAt.Local().Format(time.TimeOnly))
		}
	}()
	mask(os.Stdout, s.Token)
	step("Publishing to %s (%s) on %s%s", errp.bold(s.Repo), s.Format, p.id.base(), errp.dim(fmt.Sprintf(", session %d.", s.ID)))
	if err := cp.check(ctx, "publish/"+s.Format); err != nil {
		return err
	}

	r, ok, err := pickRow(s.Format, p.Kind, p.dir)
	if len(files) > 0 {
		// A row that takes FILE arguments too uploads them without a build, with or without a
		// project in the folder.
		for _, fr := range rowsOf(s.Format) {
			if fr.files {
				r, ok, err = fr, true, nil
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if fr, found := fileRow(s.Format); found && len(files) > 0 {
		r, ok = fr, true // FILE arguments publish without a build, also in a folder without a project
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
	if !r.takesFiles() && !r.files && len(files) > 0 {
		return fmt.Errorf("telesphorepo publish runs the %s build of %s and takes no FILE arguments; they are for raw, Debian, Arch, RPM, NuGet and PyPI repositories", r.title, p.dir)
	}
	if put.set() && s.Format != "raw" {
		return fmt.Errorf("--if-match, --if-new and --into are for raw repositories; %s is a %s repository", s.Repo, s.Format)
	}

	tmp, err := os.MkdirTemp("", "telesphorepo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := restrictDir(tmp); err != nil {
		return err
	}
	j := &job{ctx: ctx, c: c, cp: cp, p: p, s: s, tmp: tmp, files: files, put: put}
	err = r.run(j)
	var ee *exitError
	if errors.As(err, &ee) && time.Now().After(s.ExpiresAt) {
		warn("Publish session %d ended at %s (the server setting publish.session_ttl), before %s did. Uploads after that time fail.",
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
// the device proof. The challenge of the mint also shows whether the server accepts the device; a
// computer without a device that the server accepts runs the device flow, then mints. dir is the
// project folder, for the commit of a CI job.
func mint(ctx context.Context, c *client, repo, dir string) (*session, error) {
	if inCI() {
		tok, err := ciToken(ctx, c.id.base())
		if err != nil {
			return nil, err
		}
		if tok != "" {
			mask(os.Stdout, tok)
			return c.mintCI(ctx, tok, repo, ciRevision(dir))
		}
		if d, _, err := deviceOf(c.id); err != nil || d == nil {
			return nil, errors.New("this CI job has no identity token. In GitHub Actions, give the job permissions: id-token: write. " +
				"In Jenkins, bind the identity token credential to OIDC_TOKEN")
		}
	}
	d, _, err := deviceOf(c.id)
	if err != nil {
		return nil, err
	}
	if d != nil {
		nonce, _, err := c.challenge(ctx, d, false, "")
		switch {
		case err == nil:
			return c.mintNonce(ctx, d, nonce, repo)
		case !refused(err):
			return nil, err
		}
		deviceRefused(c, d, err)
	}
	step("This computer has no device of %s yet.", c.id.base())
	if d, err = loginDevice(ctx, c, deviceName()); err != nil {
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
	fmt.Fprintf(os.Stderr, "%s publishes to %s with its own command:\n", h.tool, errp.bold(repo))
	codeLine(h.command(hosts[0], repo))
	if currentCredHelpers()[hosts[0]] == helperName {
		done("The credential helper of the CLI in %s gives it the token.", errp.dim(cfg))
	} else {
		warn("Add the credential helper of the CLI to %s first (the setup line does it):", cfg)
		codeLine(credHelpersLine(hosts))
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
