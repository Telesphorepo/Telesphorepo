package cargo

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Snippets of the Cargo format (format.Spec Usage and ArtifactUsage). Cargo names a registry in
// .cargo/config.toml ([registries.<name>] index = "sparse+<url>") and reads its token from
// ~/.cargo/credentials.toml or CARGO_REGISTRIES_<NAME>_TOKEN; a private repo needs Cargo 1.74 or
// later, which sends the token on every request of a registry with auth-required, and only when
// a credential provider is configured (credential-provider = "cargo:token" on the registry).

var regNameRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// registryName is the Cargo registry name of a repo: its name with every other character as "-".
func registryName(repo string) string { return regNameRE.ReplaceAllString(repo, "-") }

// tokenEnv is the env variable Cargo reads the token of registry reg from.
func tokenEnv(reg string) string {
	return "CARGO_REGISTRIES_" + strings.ToUpper(strings.ReplaceAll(reg, "-", "_")) + "_TOKEN"
}

// indexURL is the sparse index of a repo, as Cargo names it.
func indexURL(repo *repos.Repo, baseURL string) string {
	return "sparse+" + baseURL + "/cargo/" + repo.Name + "/index/"
}

// setup returns the registry snippets of a repo for snippetContext: the registry in
// .cargo/config.toml (with source replacement of crates.io for a proxy or a group), and the
// token of a private repo.
func setup(repo *repos.Repo, baseURL, snippetContext string) []format.Snippet {
	reg := registryName(repo.Name)
	code := "[registries." + reg + "]\nindex = \"" + indexURL(repo, baseURL) + "\"\n"
	if !repo.AnonymousRead {
		code += "credential-provider = \"cargo:token\"\n"
	}
	note :="Put it in .cargo/config.toml of the project, or in ~/.cargo/config.toml for every project."
	if repo.Kind != repos.KindHosted {
		code += "\n[source.crates-io]\nreplace-with = \"" + reg + "\"\n"
		note = "Cargo then fetches every crate of crates.io through this repository. " + note
	}
	out := []format.Snippet{{ID: "cargo.registry", Tool: "cargo", Purpose: format.SnippetUse, Context: snippetContext,
		File: ".cargo/config.toml", Title: ".cargo/config.toml", Lang: "toml", Code: code, Note: note}}
	if !repo.AnonymousRead {
		out = append(out, format.Snippet{ID: "cargo.credentials", Tool: "cargo", Purpose: format.SnippetUse, Context: snippetContext,
			File: "~/.cargo/credentials.toml", Title: "~/.cargo/credentials.toml", Lang: "toml",
			Code: "[registries." + reg + "]\ntoken = \"YOUR_READ_TOKEN\"\n",
			Note: "Create a read token on Settings › Tokens. In CI, set " + tokenEnv(reg) + " instead. A private repository needs Cargo 1.74 or later."})
	}
	return out
}

// Usage returns the repo snippets: the registry setup, a dependency of a hosted repo's crate, and
// the publish command of the Telesphorepo CLI. The core adds the CI file of the tool cargo.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	out := setup(repo, baseURL, format.SnippetRepo)
	if repo.Kind == repos.KindHosted {
		out = append(out, format.Snippet{ID: "cargo.dependency", Tool: "cargo", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			File: "Cargo.toml", Title: "Cargo.toml", Lang: "toml",
			Code: "[dependencies]\nmy-crate = { version = \"1.0\", registry = \"" + registryName(repo.Name) + "\" }\n",
			Note: "my-crate and 1.0 are examples: name a crate of this repository."})
	}
	return append(out, format.CLISnippet("telesphorepo", format.CLIPublish,
		"In a Cargo project the CLI runs cargo publish to this repository. Cargo.toml stays as it is."))
}

// ciNote goes under the CI file of the Cargo tool.
const ciNote = "The CLI uses the job's identity token, so the job stores no secret. Commit telesphorepo.toml first: Set up a project on this tab writes it."

// tools are the build tools that publish to Cargo repos.
func tools() []format.Tool {
	rust := []ci.WorkflowStep{{Uses: "dtolnay/rust-toolchain@stable"}}
	return []format.Tool{{ID: "cargo", Label: "Cargo", CINote: ciNote,
		Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
			if system == ci.Jenkins {
				return ci.PublishSteps{Build: "cargo test", Publish: format.CLIPublish}
			}
			return ci.PublishSteps{Toolchain: rust, Publish: format.CLIPublish}
		}}}
}

// ArtifactUsage returns the snippets of one crate of a hosted repo. p is the crate folder
// (crates/<name>/, with version v or "" for the newest) or a .crate file in it. The pinned mode
// names the version as a Cargo requirement ("1.2.0" takes 1.2.0 and compatible updates); the
// latest mode names "*".
func (h *Handler) ArtifactUsage(ctx context.Context, repo *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	if repo.Kind != repos.KindHosted {
		return nil, nil
	}
	p = strings.Trim(p, "/")
	if n, vers, ok := parseCratePath(p); ok {
		if _, err := h.host.Assets.Get(ctx, repo.ID, p); errors.Is(err, assets.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		p, v = "crates/"+n, vers
	}
	art, lower, ok := artifactOf(p)
	if !ok {
		return nil, nil
	}
	var row versionRow
	if v != "" {
		r, found, err := h.getVersion(ctx, repo, lower, v)
		if err != nil || !found {
			return nil, err
		}
		row = r
	} else {
		_, vs, _, err := h.Versions(ctx, repo, p, "", 50)
		if err != nil || len(vs) == 0 {
			return nil, err
		}
		pick := vs[0].Version
		for _, x := range vs {
			if sv, err := version.ParseSemVer(x.Version); err == nil && !sv.Prerelease() {
				pick = x.Version
				break
			}
		}
		r, found, err := h.getVersion(ctx, repo, lower, pick)
		if err != nil || !found {
			return nil, err
		}
		row = r
	}
	art.Title, art.Name = row.name, row.name
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	reg := registryName(repo.Name)
	req, note, add := row.version, "", row.name+"@"+row.version
	if mode == format.VersionLatest {
		req, note, add = "*", "Each build takes the newest version at that moment, so builds are not reproducible. Pin a version for releases.", row.name
	}
	out := setup(repo, baseURL, format.SnippetArtifact)
	out = append(out,
		format.Snippet{ID: "cargo.dependency", Tool: "cargo", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			File: "Cargo.toml", Title: "Cargo.toml", Lang: "toml",
			Code: "[dependencies]\n" + row.name + " = { version = \"" + req + "\", registry = \"" + reg + "\" }\n", Note: note},
		format.Snippet{ID: "cargo.add", Tool: "cargo", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Add it with cargo", Lang: "sh", Code: "cargo add " + add + " --registry " + reg + "\n"})
	return &format.ArtifactUsage{Artifact: art, Version: row.version, Mode: mode, Snippets: out}, nil
}
