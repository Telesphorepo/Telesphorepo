package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// projectFileName is the project file that "setup" writes and "publish" reads. It is committed in
// the project, so teammates and CI read the same repo.
const projectFileName = "telesphorepo.toml"

// project is telesphorepo.toml:
//
//	server = "https://repo.example.com"
//	repo = "releases"
//
// kind picks the marker kind when the folder has several (e.g. "gradle"). path is the folder of
// a raw repo that "publish FILE" uploads into; dist and component are the distribution and the
// component of a Debian repo. Each [[when]] rule overrides repo, path, dist or component for a
// version that matches its glob; the first rule that matches wins (see rules.go).
type project struct {
	Server    string `toml:"server"`
	Repo      string `toml:"repo"`
	Kind      string `toml:"kind,omitempty"`
	Path      string `toml:"path,omitempty"`
	Dist      string `toml:"dist,omitempty"`
	Component string `toml:"component,omitempty"`
	When      []rule `toml:"when,omitempty"`

	dir  string   // the folder of the file
	file string   // the path of the file
	id   identity // the parsed server
}

// findProject walks up from dir to the nearest telesphorepo.toml, as git finds .git. It returns
// nil and no error when no folder holds one.
func findProject(dir string) (*project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		f := filepath.Join(dir, projectFileName)
		if _, err := os.Stat(f); err == nil {
			return readProject(f)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		up := filepath.Dir(dir)
		if up == dir {
			return nil, nil
		}
		dir = up
	}
}

// readProject reads and checks the project file f.
func readProject(f string) (*project, error) {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	p := &project{dir: filepath.Dir(f), file: f}
	if _, err := toml.Decode(string(b), p); err != nil {
		return nil, fmt.Errorf("%s is not valid TOML: %w", f, err)
	}
	if p.Server == "" || p.Repo == "" {
		return nil, fmt.Errorf(`%s needs server and repo, for example server = "https://repo.example.com" and repo = "releases"`, f)
	}
	if p.id, err = parseIdentity(p.Server); err != nil {
		return nil, fmt.Errorf("%s: %w", f, err)
	}
	for i, w := range p.When {
		if w.Version == "" || w.Repo == "" && w.Path == "" && w.Dist == "" && w.Component == "" {
			return nil, fmt.Errorf(`%s: rule %d of [[when]] needs version and at least one of repo, path, dist and component, for example version = "*-SNAPSHOT" and repo = "snapshots"`, f, i+1)
		}
	}
	return p, nil
}

// encode returns the text of the project file.
func (p *project) encode() ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// changes lists the keys whose values differ between p and the project file old, as
// "key: old -> new" lines.
func (p *project) changes(old *project) []string {
	var out []string
	for _, kv := range []struct{ key, a, b string }{
		{"server", old.Server, p.Server}, {"repo", old.Repo, p.Repo}, {"kind", old.Kind, p.Kind},
		{"path", old.Path, p.Path}, {"dist", old.Dist, p.Dist}, {"component", old.Component, p.Component},
	} {
		if kv.a != kv.b {
			out = append(out, fmt.Sprintf("  %s: %q -> %q", kv.key, kv.a, kv.b))
		}
	}
	return out
}

// writeProject writes p as telesphorepo.toml in its folder, with the [[when]] rules of the file
// that exists. A file that exists with other values is replaced only when the person says yes.
// It returns false when it kept the old file.
func writeProject(p *project) (bool, error) {
	f := filepath.Join(p.dir, projectFileName)
	p.file = f
	if old, err := readProject(f); err == nil {
		p.When = old.When
		diff := p.changes(old)
		if len(diff) == 0 {
			done("%s is up to date.", f)
			return true, nil
		}
		warn("%s has other values:", f)
		fmt.Fprintln(os.Stderr, strings.Join(diff, "\n"))
		if !confirm("Replace them?", false) {
			note("Kept %s.", f)
			return false, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(f); statErr == nil {
			warn("%v", err)
			if !confirm("Replace the file?", false) {
				return false, nil
			}
		}
	}
	b, err := p.encode()
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(f, b, 0o644); err != nil {
		return false, fmt.Errorf("cannot write %s: %w", f, err)
	}
	done("Wrote %s. Commit it, so teammates and CI publish to the same repo.", errp.bold(f))
	return true, nil
}

// gitIgnored warns when git ignores the project file in dir: CI and teammates then do not see it.
// It names the .gitignore line that matches. Without git, or outside a git work tree, it says
// nothing.
func gitIgnored(dir string) {
	out, err := exec.Command("git", "-C", dir, "check-ignore", "-v", projectFileName).Output()
	if err != nil {
		return
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return
	}
	// The form is "<source>:<line>:<pattern>\t<path>".
	rule, _, _ := strings.Cut(line, "\t")
	warn("Warning: git ignores %s (%s), so CI and teammates do not see it.", projectFileName, rule)
	cmdHint("Remove that line, or add the file with: ", "git add -f "+projectFileName)
}

// gitRoot is the top folder of the git work tree of dir, or dir outside one.
func gitRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return dir
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return filepath.FromSlash(s)
	}
	return dir
}
