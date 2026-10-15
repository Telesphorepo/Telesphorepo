package debian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Usage returns the apt setup (key, source, read credentials for a private repo) and the publish
// steps: curl from CI with the job's identity token, and curl with a publish token by hand.
// The dist "stable" and the component "main" are examples: any valid names work.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	url := baseURL + "/debian/" + repo.Name
	use := func(title, code, note string) format.Snippet {
		return format.Snippet{Client: "apt", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			Title: title, Lang: "sh", Code: code, Note: note}
	}
	if repo.Kind == repos.KindProxy {
		// A proxy serves the remote archive's files unchanged, signed with the remote's key.
		out := []format.Snippet{use("/etc/apt/sources.list.d/"+repo.Name+".list", fmt.Sprintf(
			"echo \"deb %s stable main\" \\\n"+
				"  | sudo tee /etc/apt/sources.list.d/%s.list\n"+
				"sudo apt update\n", url, repo.Name),
			"The remote archive signs the files. apt checks them with the key it already trusts for that archive, e.g. debian-archive-keyring.")}
		if !repo.AnonymousRead {
			out = append(out, use("/etc/apt/auth.conf.d/"+repo.Name+".conf", fmt.Sprintf(
				"echo \"machine %s login YOUR_USERNAME password YOUR_READ_TOKEN\" \\\n"+
					"  | sudo tee /etc/apt/auth.conf.d/%s.conf\n"+
					"sudo chmod 600 /etc/apt/auth.conf.d/%s.conf\n", url, repo.Name, repo.Name),
				"Create a read token on Settings › Tokens."))
		}
		return out
	}
	out := aptSetup(repo, baseURL, format.SnippetRepo, "stable", "main",
		"stable and main are examples: use the distribution and component the packages were uploaded to.")
	upload := "curl -fsS -u publish:$TOKEN -T hello_1.0_amd64.deb " + url + "/upload/stable/main"
	out = append(out, format.CurlCISnippets(baseURL, repo.Name, upload)...)
	return append(out, format.HandSnippet(upload))
}

// aptSetup returns the apt setup of a hosted repo for one distribution and component: the
// signing key, the source and, for a private repo, the read credentials. note goes with the
// source.
func aptSetup(repo *repos.Repo, baseURL, snippetContext, dist, component, note string) []format.Snippet {
	url := baseURL + "/debian/" + repo.Name
	creds := ""
	if !repo.AnonymousRead {
		creds = "-u YOUR_USERNAME:YOUR_READ_TOKEN "
	}
	use := func(title, code, note string) format.Snippet {
		return format.Snippet{Client: "apt", Purpose: format.SnippetUse, Context: snippetContext,
			Title: title, Lang: "sh", Code: code, Note: note}
	}
	out := []format.Snippet{
		use("Signing key", fmt.Sprintf(
			"sudo install -d -m 0755 /etc/apt/keyrings\n"+
				"sudo curl -fsSL %s%s/key.asc -o /etc/apt/keyrings/%s.asc\n", creds, url, repo.Name), ""),
		use("/etc/apt/sources.list.d/"+repo.Name+".list", fmt.Sprintf(
			"echo \"deb [signed-by=/etc/apt/keyrings/%s.asc] %s %s %s\" \\\n"+
				"  | sudo tee /etc/apt/sources.list.d/%s.list\n"+
				"sudo apt update\n", repo.Name, url, dist, component, repo.Name),
			note),
	}
	if !repo.AnonymousRead {
		// apt applies an auth.conf entry to plain http only when the scheme is spelled out.
		out = append(out, use("/etc/apt/auth.conf.d/"+repo.Name+".conf", fmt.Sprintf(
			"echo \"machine %s login YOUR_USERNAME password YOUR_READ_TOKEN\" \\\n"+
				"  | sudo tee /etc/apt/auth.conf.d/%s.conf\n"+
				"sudo chmod 600 /etc/apt/auth.conf.d/%s.conf\n", url, repo.Name, repo.Name),
			"Create a read token on Settings › Tokens."))
	}
	return out
}

// ArtifactUsage implements format.ArtifactDescriber. p is a package folder of the pool (with
// version v, or "" for the newest release, else the newest snapshot) or a .deb file in one. The
// pinned mode installs a release or a build (a file) at its version, e.g.
// "apt-get install urn=3.0~SNAPSHOT.20261014211855", and a snapshot version without a version:
// apt takes the newest. The latest mode always installs without a version.
func (h *Handler) ArtifactUsage(ctx context.Context, repo *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	if repo.Kind != repos.KindHosted {
		return nil, nil
	}
	p = strings.Trim(p, "/")
	var art format.Artifact
	var dist, component, version, pin string
	if strings.HasSuffix(p, ".deb") {
		a, err := h.assets.Get(ctx, repo.ID, p)
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
		art = format.Artifact{Path: path.Dir(p) + "/", Title: row.Package, Kind: "package"}
		dist, component, version, pin = row.Distribution, row.Component, row.Version, row.Version
	} else {
		a, comp, pkg, ok := packageOf(p)
		if !ok {
			return nil, nil
		}
		versions, err := h.readPackage(ctx, repo.ID, a, comp, pkg)
		if err != nil || len(versions) == 0 {
			return nil, err
		}
		i := slices.IndexFunc(versions, func(x *debVersion) bool {
			if v != "" {
				return x.name == v
			}
			return x.builds[0].stamp == ""
		})
		if i < 0 && v != "" {
			return nil, nil
		}
		i = max(i, 0)
		newest := versions[i].builds[0]
		art, dist, component, version = a, newest.dist, comp, versions[i].name
		if newest.stamp == "" {
			pin = newest.id
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
	out = append(out, format.Snippet{Client: "apt", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
		Title: "Install " + art.Title, Lang: "sh", Code: "sudo apt update\n" + install + "\n", Note: note})
	return &format.ArtifactUsage{Artifact: art, Version: version, Mode: mode, Snippets: out}, nil
}
