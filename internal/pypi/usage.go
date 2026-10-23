package pypi

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/pypi/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Snippets of the PyPI format (format.Spec Usage and ArtifactUsage). pip reads the credentials of
// a private index from its URL, uv from UV_INDEX_<NAME>_USERNAME and _PASSWORD, poetry from
// http-basic.<source>.

// uvEnvName is the name of a uv index in its environment variables: upper case, every character
// other than a letter or a digit "_".
func uvEnvName(n string) string {
	return strings.Map(func(r rune) rune {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return unicode.ToUpper(r)
		}
		return '_'
	}, n)
}

// setupSnippets returns how pip, uv and poetry install from repo, in snippetContext.
func setupSnippets(repo *repos.Repo, baseURL, snippetContext string) []format.Snippet {
	index := baseURL + "/pypi/" + repo.Name + "/simple/"
	private := !repo.AnonymousRead
	note := ""
	if repo.Kind == repos.KindHosted {
		note = "This repository holds your own packages. To install their dependencies from it too, use a group of it and a PyPI proxy."
	}
	add := func(out []format.Snippet, tool, key, file, title, lang, code, note string) []format.Snippet {
		return append(out, format.Snippet{ID: tool + "." + key, Tool: tool, Purpose: format.SnippetUse, Context: snippetContext,
			File: file, Title: title, Lang: lang, Code: code, Note: note})
	}
	var out []format.Snippet
	pipURL := index
	if private {
		if u, err := url.Parse(index); err == nil {
			pipURL = u.Scheme + "://YOUR_USERNAME:YOUR_READ_TOKEN@" + u.Host + u.Path
		}
	}
	pipNote := note
	if private {
		pipNote = strings.TrimSpace("Create a read token on Your account › Tokens. pip reads the credentials from the address. " + note)
	}
	pipConfText := "[global]\nindex-url = " + pipURL + "\n"
	// pip ignores an http index unless its host is trusted (or is localhost).
	if u, err := url.Parse(index); err == nil && u.Scheme == "http" {
		pipConfText += "trusted-host = " + u.Hostname() + "\n"
	}
	const pipConf = "~/.config/pip/pip.conf"
	out = add(out, "pip", "config", pipConf, pipConf, "ini", pipConfText, pipNote)

	uvName := repo.Name
	out = add(out, "uv", "index", "pyproject.toml", "pyproject.toml", "toml",
		"[[tool.uv.index]]\nname = \""+uvName+"\"\nurl = \""+index+"\"\ndefault = true\n", note)
	if private {
		env := uvEnvName(uvName)
		out = add(out, "uv", "credentials", "", "Credentials", "sh", format.ReadTokenPrompt+"\nexport UV_INDEX_"+env+"_USERNAME=YOUR_USERNAME\nexport UV_INDEX_"+env+"_PASSWORD=\"$READ_TOKEN\"\n",
			"Create a read token on Your account › Tokens. In CI, set UV_INDEX_"+env+"_USERNAME and UV_INDEX_"+env+"_PASSWORD as secrets.")
	}
	poetry := "poetry source add --priority=primary " + repo.Name + " " + index + "\n"
	if private {
		poetry += format.ReadTokenPrompt + "\npoetry config http-basic." + repo.Name + " YOUR_USERNAME \"$READ_TOKEN\"\n"
	}
	out = add(out, "poetry", "source", "", "Source", "sh", poetry, note)
	return out
}

// Usage returns the repo snippets: how pip, uv and poetry install from the repo, and how to
// publish to it from a computer with the Telesphorepo CLI. The core adds the CI files of the tools
// uv, poetry and pip.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	out := setupSnippets(repo, baseURL, format.SnippetRepo)
	if repo.Kind != repos.KindHosted {
		return out
	}
	return append(out, format.CLISnippet("telesphorepo", format.CLIPublish,
		"The CLI builds the project (poetry build with [tool.poetry] in pyproject.toml, uv build with uv.lock, else python -m build) and uploads every file of the build. telesphorepo publish dist/*.whl uploads the named files."))
}

// ciNote goes under the CI files of the PyPI tools.
const ciNote = "Commit telesphorepo.toml first: Set up a project on this tab writes it."

// tools are the build tools that publish to PyPI repos: the CLI builds with them and uploads the
// files.
func tools() []format.Tool {
	python := ci.WorkflowStep{Uses: "actions/setup-python@v6", With: [][2]string{{"python-version", "3.x"}}}
	return []format.Tool{
		{ID: "uv", Label: "uv", CINote: ciNote, Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
			if system == ci.Jenkins {
				return ci.PublishSteps{Publish: format.CLIPublish}
			}
			return ci.PublishSteps{Toolchain: []ci.WorkflowStep{{Uses: "astral-sh/setup-uv@v7"}}, Publish: format.CLIPublish}
		}},
		{ID: "poetry", Label: "Poetry", CINote: ciNote, Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
			if system == ci.Jenkins {
				return ci.PublishSteps{Publish: format.CLIPublish}
			}
			return ci.PublishSteps{Toolchain: []ci.WorkflowStep{python}, Build: "pipx install poetry", Publish: format.CLIPublish}
		}},
		{ID: "pip", Label: "pip (python -m build)", CINote: ciNote, Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
			if system == ci.Jenkins {
				return ci.PublishSteps{Build: "python3 -m pip install --user build", BuildPS: "python -m pip install --user build", Publish: format.CLIPublish}
			}
			return ci.PublishSteps{Toolchain: []ci.WorkflowStep{python}, Build: "python -m pip install build", Publish: format.CLIPublish}
		}},
	}
}

// ArtifactUsage returns the snippets of one project of a hosted repo or a proxy. p is the project
// folder packages/<project>/ or a file of the project (a proxy's under files/ or remote/). The
// pinned mode names version v (a file: its version; none: the newest release, else the newest
// version); the latest mode names none.
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	p = strings.Trim(p, "/")
	dir := p
	if from.Kind == repos.KindProxy {
		file := strings.HasPrefix(p, "files/") || strings.HasPrefix(p, "remote/")
		if file {
			d, err := parseFilename(path.Base(p))
			if err != nil {
				return nil, nil
			}
			dir, v = "packages/"+normalize(d.name), d.version.String()
		}
		art, vs, err := h.proxyProject(ctx, from, dir+"/")
		if err != nil || art == nil {
			return nil, err
		}
		if file && !slices.ContainsFunc(vs, func(x *proxyVersion) bool {
			return slices.ContainsFunc(x.files, func(f mfile) bool { return f.local == p })
		}) {
			return nil, nil
		}
		if v == "" {
			if len(vs) == 0 {
				return nil, nil
			}
			v = vs[0].version
			for _, x := range vs {
				if pv, err := version.ParsePEP440(x.version); err == nil && !pv.Prerelease() {
					v = x.version
					break
				}
			}
		}
		return artifactSnippets(repo, baseURL, art, v, mode), nil
	}
	if segs := strings.Split(p, "/"); len(segs) == 3 && segs[0] == "packages" {
		row, err := h.q.GetFile(ctx, dbq.GetFileParams{RepoID: from.ID, Filename: segs[2]})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, nil
		case err != nil:
			return nil, err
		}
		dir, v = "packages/"+segs[1], row.Version
	}
	art, project, err := h.projectOf(ctx, from, dir+"/")
	if err != nil || art == nil {
		return nil, err
	}
	if v == "" {
		rows, err := h.q.ListVersions(ctx, dbq.ListVersionsParams{RepoID: from.ID, Project: project, Limit: 100})
		if err != nil || len(rows) == 0 {
			return nil, err
		}
		v = rows[0].Version
		for _, r := range rows {
			if pv, err := version.ParsePEP440(r.Version); err == nil && !pv.Prerelease() {
				v = r.Version
				break
			}
		}
	}
	return artifactSnippets(repo, baseURL, art, v, mode), nil
}

// artifactSnippets returns the snippets of project art at version v in mode, for repo.
func artifactSnippets(repo *repos.Repo, baseURL string, art *format.Artifact, v, mode string) *format.ArtifactUsage {
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	req, note := art.Title+"=="+v, ""
	if mode == format.VersionLatest {
		req, note = art.Title, "Each install takes the newest version at that moment. Pin a version for reproducible builds."
	}
	out := setupSnippets(repo, baseURL, format.SnippetArtifact)
	for _, s := range []struct{ tool, code string }{
		{"pip", "pip install \"" + req + "\"\n"},
		{"uv", "uv add \"" + req + "\"\n"},
		{"poetry", "poetry add \"" + req + "\"\n"},
	} {
		out = append(out, format.Snippet{ID: s.tool + ".install", Tool: s.tool, Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Install " + art.Title, Lang: "sh", Code: s.code, Note: note})
	}
	return &format.ArtifactUsage{Artifact: *art, Version: v, Mode: mode, Snippets: order(out)}
}

// order puts the snippets of each tool together, in the order the tools first appear.
func order(in []format.Snippet) []format.Snippet {
	var tools []string
	by := map[string][]format.Snippet{}
	for _, s := range in {
		if _, ok := by[s.Tool]; !ok {
			tools = append(tools, s.Tool)
		}
		by[s.Tool] = append(by[s.Tool], s)
	}
	out := make([]format.Snippet, 0, len(in))
	for _, t := range tools {
		out = append(out, by[t]...)
	}
	return out
}
