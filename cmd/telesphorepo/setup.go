package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// setupCodePrefix starts every setup code.
const setupCodePrefix = "trsu_"

// parseLink reads a setup link "<server>/#/setup/<code>": the server from the URL, the code from
// the fragment. A browser that opens the link never sends the fragment to the server.
func parseLink(raw string) (identity, string, error) {
	bad := fmt.Errorf("the setup link must look like https://repo.example.com/#/setup/%s... Copy the setup line from the Publish tab of the repository", setupCodePrefix)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return identity{}, "", bad
	}
	code, ok := strings.CutPrefix(u.Fragment, "/setup/")
	if !ok || !strings.HasPrefix(code, setupCodePrefix) || len(code) <= len(setupCodePrefix) {
		return identity{}, "", bad
	}
	u.Fragment, u.RawFragment = "", ""
	id, err := parseIdentity(strings.TrimSuffix(u.String(), "#"))
	if err != nil {
		return identity{}, "", err
	}
	return id, code, nil
}

// setupResult is the answer of POST /api/devices/setup.
type setupResult struct {
	DeviceID int64  `json:"device_id"`
	User     string `json:"user"`
	Repo     string `json:"repo"`
	Format   string `json:"format"`
	Kind     string `json:"kind"`
}

// setup sets up the project of the current folder from a setup link: the device, the project
// file or the hook of the format, and on a terminal a CI file.
func setup(ctx context.Context, args []string) error {
	name, rest, err := nameFlag("setup", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usage("setup")
	}
	id, code, err := parseLink(rest[0])
	if err != nil {
		return err
	}
	if name == "" {
		name = deviceName()
	}
	c := newClient(id)
	if err := attach(c, false).check(ctx, "setup"); err != nil {
		return err
	}
	res, d, err := setupDevice(ctx, c, code, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "publishing to %s (%s) as %s\n", res.Repo, res.Format, res.User)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var ciTool string
	if h, ok := helperFormats[res.Format]; ok {
		if err := setupHook(c, h, res.Repo); err != nil {
			return err
		}
		ciTool = res.Format
		// A Helm chart in this folder publishes with "telesphorepo publish", so it gets the
		// project file too.
		if r, ok, _ := pickRow(res.Format, "", cwd); ok {
			if err := setupProject(id, res, r, cwd, false); err != nil {
				return err
			}
			ciTool = r.ciToolFor(cwd)
		}
	} else {
		if len(rowsOf(res.Format)) == 0 {
			return fmt.Errorf("this CLI cannot set up %s repositories. Run: telesphorepo update %s", res.Format, id.base())
		}
		several := len(markerRows(res.Format, cwd)) > 1
		r, ok, err := pickRow(res.Format, "", cwd)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no project of a %s repository in %s: telesphorepo setup looks for %s. Run the setup line in the project folder",
				res.Format, cwd, markersOf(res.Format))
		}
		if err := setupProject(id, res, r, cwd, several); err != nil {
			return err
		}
		ciTool = r.ciToolFor(cwd)
	}
	if interactive() {
		return setupCI(ctx, c, d, res.Repo, ciTool, cwd)
	}
	return nil
}

// markerRows returns the rows of format that have a marker in dir.
func markerRows(format, dir string) []row {
	var out []row
	for _, r := range rowsOf(format) {
		if !r.takesFiles() && r.marker(dir) != "" {
			out = append(out, r)
		}
	}
	return out
}

// setupDevice takes the setup code with the device of this computer when the server accepts it,
// else with a new device key, which it saves. It refreshes hosts.json.
func setupDevice(ctx context.Context, c *client, code, name string) (*setupResult, *deviceFile, error) {
	d, dir, err := usableDevice(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	var res setupResult
	if d != nil {
		nonce, _, err := c.challenge(ctx, d, false, "")
		if err != nil {
			return nil, nil, err
		}
		id := strconv.FormatInt(d.DeviceID, 10)
		_, err = c.call(ctx, request{method: http.MethodPost, path: "/api/devices/setup", json: map[string]any{
			"code": code, "device_id": d.DeviceID, "nonce": nonce, "signature": d.sign(setupPrefix + nonce + "\n" + id),
		}, out: &res})
		if err != nil {
			return nil, nil, err
		}
	} else {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		_, err = c.call(ctx, request{method: http.MethodPost, path: "/api/devices/setup", json: map[string]any{
			"code": code, "name": name, "public_key": base64.RawURLEncoding.EncodeToString(pub),
		}, out: &res})
		if err != nil {
			return nil, nil, err
		}
		if d, err = newDevice(c.id, res.DeviceID, key); err != nil {
			return nil, nil, err
		}
		f, err := d.save(dir)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(os.Stderr, "This computer is device %d of %s. Its key is in %s\n", d.DeviceID, c.id.base(), f)
	}
	saveRegistryHosts(ctx, c, dir)
	return &res, d, nil
}

// setupProject writes telesphorepo.toml for the row r in dir, and warns when git ignores it.
// kind goes into the file when the folder has several marker kinds.
func setupProject(id identity, res *setupResult, r row, dir string, kind bool) error {
	p := &project{Server: id.base(), Repo: res.Repo, dir: dir}
	old, _ := readProject(filepath.Join(dir, projectFileName))
	if kind {
		p.Kind = r.kind
	}
	switch res.Format {
	case "raw":
		def := ""
		if old != nil {
			def = old.Path
		}
		p.Path = def
		if interactive() {
			p.Path = strings.Trim(ask(fmt.Sprintf("Folder in %s for the files of telesphorepo publish FILE [%s]: ", res.Repo, orRoot(def)), def), "/")
		}
	case "debian":
		dist, comp := "stable", "main"
		if old != nil && old.Dist != "" {
			dist, comp = old.Dist, old.Component
		}
		p.Dist, p.Component = dist, comp
		if interactive() {
			p.Dist = ask(fmt.Sprintf("Distribution of the packages [%s]: ", dist), dist)
			p.Component = ask(fmt.Sprintf("Component of the packages [%s]: ", comp), comp)
		} else {
			fmt.Fprintf(os.Stderr, "Packages go to distribution %s, component %s. Change dist and component in %s to change that.\n", dist, comp, projectFileName)
		}
	}
	if _, err := writeProject(p); err != nil {
		return err
	}
	gitIgnored(dir)
	switch {
	case r.takesFiles():
		fmt.Fprintln(os.Stderr, "Publish with: telesphorepo publish FILE...")
	default:
		fmt.Fprintln(os.Stderr, "Publish with: telesphorepo publish")
	}
	return nil
}

func orRoot(p string) string {
	if p == "" {
		return "the root"
	}
	return p
}

// setupHook shows the credential helper hook of a format of the helper category and adds it to
// the tool's config file when the person says yes.
func setupHook(c *client, h helperFormat, repo string) error {
	dir, err := serverDir(c.id)
	if err != nil {
		return err
	}
	hosts := dockerHosts(c.id, dir)
	cfg, err := dockerConfigPath()
	if err != nil {
		return err
	}
	line := credHelpersLine(hosts)
	fmt.Fprintf(os.Stderr, "%s gets the token from the credential helper of the CLI with this entry in %s:\n  %s\n", h.tool, cfg, line)
	if confirm(fmt.Sprintf("Add the credential helper to %s?", cfg), false) {
		if _, err := addCredHelpers(hosts); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Added it to %s.\n", cfg)
	} else {
		fmt.Fprintf(os.Stderr, "Kept %s. Add the entry yourself to push:\n  %s\n", cfg, line)
	}
	fmt.Fprintf(os.Stderr, "Push with: %s\n", h.command(hosts[0], repo))
	if len(hosts) > 1 {
		fmt.Fprintf(os.Stderr, "Helm 4 and ORAS push to the upload host: helm push CHART.tgz oci://%s/%s\n", hosts[1], repo)
	}
	return nil
}

// ciSystems are the CI files that setup offers, with their system of GET /api/repos/{name}/ci.
var ciSystems = []struct{ label, system string }{
	{"No CI file", ""},
	{"GitHub Actions", "github-actions"},
	{"Jenkinsfile", "jenkins"},
	{"Jenkins freestyle job", "jenkins-freestyle"},
}

// ciSnippet is the answer of GET /api/repos/{name}/ci.
type ciSnippet struct {
	File  string `json:"file"`
	Title string `json:"title"`
	Code  string `json:"code"`
	Note  string `json:"note"`
}

// setupCI asks for a CI file and writes the one that the server gives for tool. It shows the
// file first; a change to a file that exists needs a yes.
func setupCI(ctx context.Context, c *client, d *deviceFile, repo, tool, dir string) error {
	if tool == "" {
		fmt.Fprintln(os.Stderr, "The server has no CI file for this project. In CI, install the CLI and run telesphorepo publish.")
		return nil
	}
	labels := make([]string, len(ciSystems))
	for i, s := range ciSystems {
		labels[i] = s.label
	}
	sys := ciSystems[choose("Add a CI file that publishes to "+repo+"?", labels, 0)].system
	if sys == "" {
		return nil
	}
	s, err := c.mintDevice(ctx, d, repo)
	if err != nil {
		return err
	}
	defer c.closeSession(s)
	var sn ciSnippet
	q := url.Values{"system": {sys}, "tool": {tool}}
	if _, err := c.call(ctx, request{method: http.MethodGet, path: "/api/repos/" + url.PathEscape(repo) + "/ci?" + q.Encode(),
		token: s.Token, out: &sn}); err != nil {
		return err
	}
	defer func() {
		if sn.Note != "" {
			fmt.Fprintln(os.Stderr, sn.Note)
		}
	}()
	if sn.File == "" {
		fmt.Fprintf(os.Stderr, "Paste this into the shell build step of the job:\n\n%s\n", sn.Code)
		return nil
	}
	root := gitRoot(dir)
	target := filepath.Join(root, filepath.FromSlash(sn.File))
	fmt.Fprintf(os.Stderr, "--- %s ---\n%s--- end ---\n", target, sn.Code)
	old, err := os.ReadFile(target)
	switch {
	case err == nil && bytes.Equal(old, []byte(sn.Code)):
		fmt.Fprintf(os.Stderr, "%s is up to date.\n", target)
		return nil
	case err == nil:
		if !confirm(fmt.Sprintf("%s exists. Replace it?", target), false) {
			fmt.Fprintf(os.Stderr, "Kept %s.\n", target)
			return nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, []byte(sn.Code), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", target, unwrapPath(err))
	}
	fmt.Fprintf(os.Stderr, "Wrote %s.\n", target)
	if rel, err := filepath.Rel(root, dir); err == nil && rel != "." {
		fmt.Fprintf(os.Stderr, "The CI job runs in the top folder of the repository: put cd %s before telesphorepo publish.\n", filepath.ToSlash(rel))
	}
	return nil
}
