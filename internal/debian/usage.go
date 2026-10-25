package debian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// usageVersion returns the version of package r that the usage of a package folder names:
// version v, else the newest version whose newest build is a release, else the newest version;
// nil when there is none.
func (h *Handler) usageVersion(ctx context.Context, r pkgRef, v string) (*debVersion, error) {
	if v != "" {
		return h.versionOf(ctx, r, v)
	}
	var first *debVersion
	for below := top; ; {
		page, err := h.versionsPage(ctx, r, below, 100)
		if err != nil || len(page) == 0 {
			return first, err
		}
		if first == nil {
			first = page[0]
		}
		for _, x := range page {
			if x.stamp == "" {
				return x, nil
			}
		}
		below = page[len(page)-1].key
	}
}

// Usage returns the apt setup (key, source, read credentials for a private repo) and the publish
// command of the Telesphorepo CLI. The core adds the CI files of the tool telesphorepo. The dist
// "stable" and the component "main" are examples: any valid names work.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	url := baseURL + "/debian/" + repo.Name
	if repo.Kind == repos.KindProxy {
		// A proxy serves the remote archive's files unchanged, signed with the remote's key.
		file := "/etc/apt/sources.list.d/" + repo.Name + ".list"
		out := []format.Snippet{{ID: "apt.source", Tool: "apt", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			File: file, Title: file, Lang: "sh", Code: fmt.Sprintf(
				"echo \"deb %s stable main\" \\\n"+
					"  | sudo tee /etc/apt/sources.list.d/%s.list\n"+
					"sudo apt update\n", url, repo.Name),
			Note: "The remote archive signs the files. apt checks them with the key it already trusts for that archive, e.g. debian-archive-keyring."}}
		if !repo.AnonymousRead {
			out = append(out, aptAuth(url, repo.Name, format.SnippetRepo))
		}
		return out
	}
	out := aptSetup(repo, baseURL, format.SnippetRepo, "stable", "main",
		"stable and main are examples: use the distribution and component the packages were uploaded to.")
	return append(out, format.CLISnippet("telesphorepo", format.CLIPublish+" hello_1.0_amd64.deb",
		"The CLI uploads each package to the distribution and component that dist and component name in telesphorepo.toml."))
}

// aptSetup returns the apt setup of a hosted repo for one distribution and component: the
// signing key, the source and, for a private repo, the read credentials. note goes with the
// source.
func aptSetup(repo *repos.Repo, baseURL, snippetContext, dist, component, note string) []format.Snippet {
	url := baseURL + "/debian/" + repo.Name
	key := fmt.Sprintf("sudo curl -fsSL %s/key.asc -o /etc/apt/keyrings/%s.asc\n", url, repo.Name)
	if !repo.AnonymousRead {
		key = format.ReadTokenPrompt + "\n" + format.ReadTokenConfig + " \\\n  | " +
			fmt.Sprintf("sudo curl -fsSL -K - %s/key.asc -o /etc/apt/keyrings/%s.asc\n", url, repo.Name)
	}
	source := "/etc/apt/sources.list.d/" + repo.Name + ".list"
	out := []format.Snippet{
		{ID: "apt.key", Tool: "apt", Purpose: format.SnippetUse, Context: snippetContext,
			Title: "Signing key", Lang: "sh", Code: "sudo install -d -m 0755 /etc/apt/keyrings\n" + key},
		{ID: "apt.source", Tool: "apt", Purpose: format.SnippetUse, Context: snippetContext,
			File: source, Title: source, Lang: "sh", Code: fmt.Sprintf(
				"echo \"deb [signed-by=/etc/apt/keyrings/%s.asc] %s %s %s\" \\\n"+
					"  | sudo tee /etc/apt/sources.list.d/%s.list\n"+
					"sudo apt update\n", repo.Name, url, dist, component, repo.Name),
			Note: note},
	}
	if !repo.AnonymousRead {
		// apt applies an auth.conf entry to plain http only when the scheme is spelled out.
		out = append(out, aptAuth(url, repo.Name, snippetContext))
	}
	return out
}

// aptAuth returns the apt read credentials of a private repo. The file gets mode 600 before
// the token is in it, the token comes from a hidden prompt, and tee writes nothing to stdout.
func aptAuth(url, repo, snippetContext string) format.Snippet {
	file := "/etc/apt/auth.conf.d/" + repo + ".conf"
	return format.Snippet{ID: "apt.credentials", Tool: "apt", Purpose: format.SnippetUse, Context: snippetContext,
		File: file, Title: file, Lang: "sh", Code: format.ReadTokenPrompt + "\n" +
			"sudo install -m 600 /dev/null " + file + "\n" +
			"printf 'machine %s login YOUR_USERNAME password %s\\n' " + url + " \"$READ_TOKEN\" \\\n" +
			"  | sudo tee -a " + file + " > /dev/null\n",
		Note: "Create a read token on Your account › Tokens. Enter it at the prompt."}
}

// ArtifactUsage returns the snippets of one package. p is a package folder of the pool (with
// version v, or "" for the newest release, else the newest snapshot) or a .deb file in one. The
// pinned mode installs a release or a build (a file) at its version, e.g.
// "apt-get install urn=3.0~SNAPSHOT.20261014211855", and a snapshot version without a version:
// apt takes the newest. The latest mode always installs without a version.
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	if from.Kind != repos.KindHosted {
		return nil, nil
	}
	p = strings.Trim(p, "/")
	var art format.Artifact
	var dist, component, version, pin string
	if strings.HasSuffix(p, ".deb") {
		a, err := h.assets.Get(ctx, from.ID, p)
		if errors.Is(err, assets.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		row, err := h.q.GetPackageOfAsset(ctx, a.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		art = format.Artifact{Path: path.Dir(p) + "/", Title: row.Package, Name: row.Package, Kind: "package"}
		dist, component, version, pin = row.Distribution, row.Component, row.Version, row.Version
	} else {
		a, comp, pkg, ok := packageOf(p)
		if !ok {
			return nil, nil
		}
		ver, err := h.usageVersion(ctx, refOf(from, a, comp, pkg), v)
		if err != nil || ver == nil {
			return nil, err
		}
		art, dist, component, version = a, ver.dist, comp, ver.name
		if ver.stamp == "" {
			pin = ver.newest
		}
	}
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	install, note := "sudo apt-get install "+art.Title, "apt installs the newest version of the package."
	if mode == format.VersionPinned && pin != "" {
		install, note = "sudo apt-get install "+art.Title+"="+pin, ""
	}
	out := aptSetup(repo, baseURL, format.SnippetArtifact, dist, component, "")
	out = append(out, format.Snippet{ID: "apt.install", Tool: "apt", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
		Title: "Install " + art.Title, Lang: "sh", Code: "sudo apt update\n" + install + "\n", Note: note})
	return &format.ArtifactUsage{Artifact: art, Version: version, Mode: mode, Snippets: out}, nil
}
