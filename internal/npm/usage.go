package npm

import (
	"context"
	"net/url"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Snippets of the npm format (format.Spec Usage and ArtifactUsage). npm, pnpm and bun read the
// registry and the token from .npmrc, where the auth line keys on the registry URL without its
// scheme; each expands ${NPM_TOKEN} from the env. Yarn (berry) reads .yarnrc.yml, sends the
// token on reads only with npmAlwaysAuth, and reaches a plain-HTTP registry only through
// unsafeHttpWhitelist.

// registryURL is the registry URL of repo, with a trailing slash.
func registryURL(baseURL string, repo *repos.Repo) string { return baseURL + "/npm/" + repo.Name + "/" }

type snippets struct {
	repo     *repos.Repo
	registry string
	context  string
	out      []format.Snippet
}

func (s *snippets) add(tool, key, file, title, lang, code, note string) {
	s.out = append(s.out, format.Snippet{ID: tool + "." + key, Tool: tool, Purpose: format.SnippetUse, Context: s.context,
		File: file, Title: title, Lang: lang, Code: code, Note: note})
}

func (s *snippets) private() bool { return !s.repo.AnonymousRead }

// setup adds the .npmrc and .yarnrc.yml of the repo.
func (s *snippets) setup() {
	npmrc := "registry=" + s.registry + "\n"
	note := "npm, pnpm and bun read the .npmrc of the project."
	if s.private() {
		npmrc += "//" + strings.TrimPrefix(strings.TrimPrefix(s.registry, "https://"), "http://") + ":_authToken=${NPM_TOKEN}\n"
		note += " Create a read token on Settings › Tokens and set NPM_TOKEN to it."
	}
	if s.repo.Kind == repos.KindHosted {
		note += " This repository holds your own packages only: to install public packages through the same URL, use a group of it and a proxy of registry.npmjs.org."
	}
	s.add("npm", "npmrc", ".npmrc", ".npmrc", "ini", npmrc, note)
	yarnrc := "npmRegistryServer: \"" + s.registry + "\"\n"
	if s.private() {
		yarnrc += "npmAlwaysAuth: true\nnpmAuthToken: \"${NPM_TOKEN}\"\n"
	}
	if u, err := url.Parse(s.registry); err == nil && u.Scheme == "http" {
		yarnrc += "unsafeHttpWhitelist:\n  - \"" + u.Hostname() + "\"\n"
	}
	ynote := "Yarn 2 and later read .yarnrc.yml."
	// Yarn 4 installs a version only once it is a week old (npmMinimalAgeGate). A hosted repo
	// holds only your own packages, so its snippet turns the gate off.
	if s.repo.Kind == repos.KindHosted {
		yarnrc += "npmMinimalAgeGate: 0\n"
	} else {
		ynote += " Yarn 4 installs a version only once it is a week old; list your own packages in npmPreapprovedPackages to install them at once."
	}
	if s.private() {
		ynote += " Set NPM_TOKEN to a read token."
	}
	s.add("yarn", "yarnrc", ".yarnrc.yml", ".yarnrc.yml", "yaml", yarnrc, ynote)
}

// Usage returns the repo snippets: the registry setup of npm, pnpm, bun and Yarn, and how to
// publish with the Telesphorepo CLI. The core adds the CI files of the tools.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	s := &snippets{repo: repo, registry: registryURL(baseURL, repo), context: format.SnippetRepo}
	s.setup()
	return append(s.out, format.CLISnippet("telesphorepo", format.CLIPublish,
		"In the project folder the CLI runs the publish of the project's package manager, chosen by the lockfile: pnpm publish, yarn npm publish, bun publish, else npm publish."))
}

// ArtifactUsage returns the install snippets of one package: p is its folder or a file of it.
// The pinned mode names version v, else the latest tag (a hosted repo) or none.
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	pkg, file, isFile := splitFile(strings.Trim(p, "/"))
	if !isFile {
		_, n, ok := artifactOf(p)
		if !ok {
			return nil, nil
		}
		pkg = n
	} else if v == "" {
		v = strings.TrimSuffix(strings.TrimPrefix(file, unscoped(pkg)+"-"), ".tgz")
	}
	art, _, _ := artifactOf(pkg + "/")
	if v == "" && from.Kind == repos.KindHosted {
		tags, err := h.q.ListTags(ctx, dbq.ListTagsParams{RepoID: from.ID, Name: pkg})
		if err != nil {
			return nil, err
		}
		if len(tags) == 0 {
			return nil, nil
		}
		for _, t := range tags {
			if t.Tag == "latest" {
				v = t.Version
			}
		}
	}
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	spec, note := pkg, ""
	if mode == format.VersionPinned && v != "" {
		spec = pkg + "@" + v
	} else if mode == format.VersionLatest {
		note = "The install takes the version of the latest tag and records the range in package.json; the lockfile pins it."
	}
	s := &snippets{repo: repo, registry: registryURL(baseURL, repo), context: format.SnippetArtifact}
	s.setup()
	s.add("npm", "install", "", "Install", "sh", "npm install "+spec+"\n", note)
	s.add("yarn", "install", "", "Install", "sh", "yarn add "+spec+"\n", note)
	out := s.out
	// Each tool's snippets together.
	ordered := []format.Snippet{out[0], out[2], out[1], out[3]}
	return &format.ArtifactUsage{Artifact: art, Version: v, Mode: mode, Snippets: ordered}, nil
}

// ciNote goes under the CI files of the npm tools.
const ciNote = "The CLI uses the job's identity token, so the job stores no secret. Commit telesphorepo.toml first: Set up a project on this tab writes it."

// tools are the package managers that publish to npm repos, each through telesphorepo publish.
func tools() []format.Tool {
	node := ci.WorkflowStep{Uses: "actions/setup-node@v5", With: [][2]string{{"node-version", "lts/*"}}}
	tool := func(id, label, install string, steps ...ci.WorkflowStep) format.Tool {
		return format.Tool{ID: id, Label: label, CINote: ciNote, Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
			if system == ci.Jenkins {
				return ci.PublishSteps{Build: install, Publish: format.CLIPublish}
			}
			return ci.PublishSteps{Toolchain: steps, Build: install, Publish: format.CLIPublish}
		}}
	}
	return []format.Tool{
		tool("npm", "npm", "npm ci", node),
		tool("pnpm", "pnpm", "pnpm install --frozen-lockfile", ci.WorkflowStep{Uses: "pnpm/action-setup@v4"}, node),
		tool("yarn", "Yarn", "corepack enable && yarn install --immutable", node),
		tool("bun", "Bun", "bun install --frozen-lockfile", ci.WorkflowStep{Uses: "oven-sh/setup-bun@v2"}),
	}
}
