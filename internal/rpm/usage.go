package rpm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// metadataExpire is how long dnf keeps the repodata before it asks again: a new release reaches
// "dnf upgrade" within it, for one request of repomd.xml per machine (dnf's default is 48 hours).
const metadataExpire = "1h"

// Usage returns the dnf setup of a repo (the key and the .repo file, with the read credentials of
// a private repo) and the publish command of the Telesphorepo CLI. The core adds the CI files of
// the tool telesphorepo.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	if repo.Kind == repos.KindProxy {
		return proxyUsage(repo, baseURL)
	}
	out := dnfSetup(repo, baseURL, format.SnippetRepo)
	return append(out, format.CLISnippet("telesphorepo", format.CLIPublish+" anchor-1.0-1.x86_64.rpm",
		"The server reads each package's name, version and architecture from the package, and signs it."))
}

// repoFile writes the .repo file of a repo: lines with %s for the read token when private.
func repoFile(id, baseurl, gpgkey string, private bool) []string {
	lines := []string{"[" + id + "]", "name=" + id, "baseurl=" + baseurl, "gpgcheck=1", "repo_gpgcheck=1",
		"gpgkey=" + gpgkey, "metadata_expire=" + metadataExpire}
	if private {
		lines = append(lines, "username=YOUR_USERNAME", "password=%s")
	}
	return lines
}

// writeRepoFile is the shell that writes the .repo file at file: with tee for a public repo, and
// for a private one into a file only root reads, with the token from the prompt.
func writeRepoFile(file string, lines []string, private bool) string {
	if !private {
		return "sudo tee " + file + " > /dev/null <<'EOF'\n" + strings.Join(lines, "\n") + "\nEOF\n"
	}
	return "sudo install -m 600 /dev/null " + file + "\n" +
		fmt.Sprintf("printf '%s\\n' \"$READ_TOKEN\" \\\n  | sudo tee %s > /dev/null\n", strings.Join(lines, `\n`), file)
}

// dnfSetup returns the setup of a hosted repo: the signing key that rpm and dnf trust, and the
// .repo file with gpgcheck and repo_gpgcheck. A private repo's key is downloaded with the read
// credentials (dnf5 sends no credentials when it fetches a gpgkey URL), and its .repo file holds
// them in a file only root reads.
func dnfSetup(repo *repos.Repo, baseURL, snippetContext string) []format.Snippet {
	url := baseURL + "/rpm/" + repo.Name
	file := "/etc/yum.repos.d/" + repo.Name + ".repo"
	private := !repo.AnonymousRead
	key, gpgkey := url+"/key.asc", url+"/key.asc"
	code := "sudo rpm --import " + key + "\n"
	if private {
		keyFile := "/etc/pki/rpm-gpg/RPM-GPG-KEY-" + repo.Name
		gpgkey = "file://" + keyFile
		code = format.ReadTokenPrompt + "\n" + format.ReadTokenConfig + " \\\n  | " +
			fmt.Sprintf("curl -fsSL -K - %s | sudo tee %s > /dev/null\n", key, keyFile) +
			"sudo rpm --import " + keyFile + "\n"
	}
	code += writeRepoFile(file, repoFile(repo.Name, url+"/", gpgkey, private), private)
	note := "dnf checks the signature of each package and of the repodata with the repository's key. The repodata expires after an hour, so dnf upgrade sees a new release within it."
	if private {
		note += " Create a read token on Your account › Tokens and enter it at the prompt."
	}
	return []format.Snippet{{ID: "dnf.repo", Tool: "dnf", Purpose: format.SnippetUse, Context: snippetContext, File: file,
		Title: file, Lang: "sh", Code: code, Note: note}}
}

// proxyUsage is the setup of a proxy: a .repo file whose baseurl is the proxy followed by the
// remote's own layout, with the remote's keys.
func proxyUsage(repo *repos.Repo, baseURL string) []format.Snippet {
	file := "/etc/yum.repos.d/" + repo.Name + ".repo"
	private := !repo.AnonymousRead
	lines := []string{"[" + repo.Name + "]", "name=" + repo.Name,
		"baseurl=" + baseURL + "/rpm/" + repo.Name + "/releases/$releasever/Everything/$basearch/os/",
		"gpgcheck=1", "gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-$releasever-$basearch"}
	if private {
		lines = append(lines, "username=YOUR_USERNAME", "password=%s")
	}
	code := writeRepoFile(file, lines, private)
	if private {
		code = format.ReadTokenPrompt + "\n" + code
	}
	note := "After the repository comes the remote's own layout; this one is a Fedora mirror's, with dnf's $releasever and $basearch. dnf checks the packages with the keys of the remote's packagers."
	if private {
		note += " Create a read token on Your account › Tokens and enter it at the prompt."
	}
	return []format.Snippet{{ID: "dnf.repo", Tool: "dnf", Purpose: format.SnippetUse, Context: format.SnippetRepo, File: file,
		Title: file, Lang: "sh", Code: code, Note: note}}
}

// ArtifactUsage returns the snippets of one package. p is a package folder (with version v, or ""
// for the newest) or a package file in one. The pinned mode installs that version by its name,
// version and release (every version is in the repodata); the latest mode installs the newest.
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	if from.Kind != repos.KindHosted {
		return nil, nil
	}
	p = strings.Trim(p, "/")
	var art format.Artifact
	var ver string
	if strings.HasSuffix(p, ".rpm") {
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
		art = format.Artifact{Path: "Packages/" + row.Name + "/", Title: row.Name, Name: row.Name, Kind: "package"}
		ver = row.Evr
	} else {
		a, pkg, ok := packageOf(p)
		if !ok {
			return nil, nil
		}
		below := top
		if v != "" {
			k, ok := keyOf(v)
			if !ok {
				return nil, nil
			}
			below = append(k, 0)
		}
		rows, err := h.q.VersionsPage(ctx, from.ID, pkg, below, 1)
		if err != nil || len(rows) == 0 {
			return nil, err
		}
		vs := group(rows)
		if v != "" && vs[0].name != v {
			return nil, nil
		}
		art, ver = a, vs[0].name
	}
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	out := dnfSetup(repo, baseURL, format.SnippetArtifact)
	install := format.Snippet{ID: "dnf.install", Tool: "dnf", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
		Title: "Install " + art.Title, Lang: "sh", Code: "sudo dnf install " + art.Title + "\n", Note: "dnf installs the newest version of the package, and dnf upgrade keeps it current."}
	if mode == format.VersionPinned {
		install.Code = "sudo dnf install " + art.Title + "-" + ver + "\n"
		install.Note = "dnf installs this version; dnf upgrade moves it to the newest."
	}
	out = append(out, install)
	return &format.ArtifactUsage{Artifact: art, Version: ver, Mode: mode, Snippets: out}, nil
}
