package arch

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

// Usage returns the pacman setup of a repo (the key, the section of pacman.conf and, for a
// private repo, the read credentials) and the publish command of the Telesphorepo CLI. The core
// adds the CI files of the tool telesphorepo.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	if repo.Kind == repos.KindProxy {
		return proxyUsage(repo, baseURL)
	}
	out := h.pacmanSetup(repo, baseURL, format.SnippetRepo)
	return append(out, format.CLISnippet("telesphorepo", format.CLIPublish+" anchor-1.0-1-x86_64.pkg.tar.zst",
		"The server reads each package's name, version and architecture from the package."))
}

// withCredentials puts user information before the host of url: https://USER:%s@host/...
func withCredentials(url string) string {
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return url
	}
	return scheme + "://YOUR_USERNAME:%s@" + rest
}

// proxyUsage is the setup of a proxy of an Arch mirror: the first server of the mirror list.
func proxyUsage(repo *repos.Repo, baseURL string) []format.Snippet {
	server := baseURL + "/arch/" + repo.Name + "/$repo/os/$arch"
	code := fmt.Sprintf("sudo sed -i '1i Server = %s' /etc/pacman.d/mirrorlist\nsudo pacman -Syu\n", server)
	note := "pacman checks the packages with the keys it trusts for the remote, e.g. archlinux-keyring. The path after the repository is the remote's layout."
	if !repo.AnonymousRead {
		code = format.ReadTokenPrompt + "\n" + "sudo install -m 600 /dev/null /etc/pacman.d/mirrorlist.new\n" +
			fmt.Sprintf("{ printf 'Server = %s\\n' \"$READ_TOKEN\"; cat /etc/pacman.d/mirrorlist; } \\\n  | sudo tee /etc/pacman.d/mirrorlist.new > /dev/null\n", withCredentials(server)) +
			"sudo mv /etc/pacman.d/mirrorlist.new /etc/pacman.d/mirrorlist\nsudo pacman -Syu\n"
		note += " Create a read token on Your account › Tokens and enter it at the prompt; percent-encode an @ in your user name."
	}
	return []format.Snippet{{ID: "pacman.mirrorlist", Tool: "pacman", Purpose: format.SnippetUse, Context: format.SnippetRepo,
		File: "/etc/pacman.d/mirrorlist", Title: "/etc/pacman.d/mirrorlist", Lang: "sh", Code: code, Note: note}}
}

// pacmanSetup returns the pacman setup of a hosted repo: the signing key, the section of
// /etc/pacman.conf and, for a private repo, the server line with read credentials in a file only
// root reads.
func (h *Handler) pacmanSetup(repo *repos.Repo, baseURL, snippetContext string) []format.Snippet {
	url := baseURL + "/arch/" + repo.Name
	keyFile := "/tmp/" + repo.Name + ".asc"
	get := fmt.Sprintf("curl -fsSL %s/key.asc -o %s\n", url, keyFile)
	if !repo.AnonymousRead {
		get = format.ReadTokenPrompt + "\n" + format.ReadTokenConfig + " \\\n  | " + fmt.Sprintf("curl -fsSL -K - %s/key.asc -o %s\n", url, keyFile)
	}
	fpr, keyNote := "FINGERPRINT", "The repository has no signing key yet: set one on its Signing tab."
	if k, ok := h.keys.Get(signingKey(repo)); ok {
		fpr, keyNote = k.Fingerprint, ""
	}
	server := url + "/$arch"
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		server = url[:i+1] + "$repo/$arch"
	}
	section := fmt.Sprintf("\n[%s]\nSigLevel = Required\nServer = %s\n", repo.Name, server)
	if !repo.AnonymousRead {
		section = fmt.Sprintf("\n[%s]\nSigLevel = Required\nInclude = /etc/pacman.d/%s-server\n", repo.Name, repo.Name)
	}
	out := []format.Snippet{
		{ID: "pacman.key", Tool: "pacman", Purpose: format.SnippetUse, Context: snippetContext, Title: "Signing key", Lang: "sh",
			Code: get + "sudo pacman-key --add " + keyFile + "\nsudo pacman-key --lsign-key " + fpr + "\n", Note: keyNote},
		{ID: "pacman.conf", Tool: "pacman", Purpose: format.SnippetUse, Context: snippetContext, File: "/etc/pacman.conf",
			Title: "/etc/pacman.conf", Lang: "sh", Code: "sudo tee -a /etc/pacman.conf > /dev/null <<'EOF'" + section + "EOF\nsudo pacman -Syu\n",
			Note: "pacman names the section after the repository, and checks every package and index with its key."},
	}
	if !repo.AnonymousRead {
		file := "/etc/pacman.d/" + repo.Name + "-server"
		out = append(out, format.Snippet{ID: "pacman.credentials", Tool: "pacman", Purpose: format.SnippetUse, Context: snippetContext,
			File: file, Title: file, Lang: "sh", Code: format.ReadTokenPrompt + "\n" +
				"sudo install -m 600 /dev/null " + file + "\n" +
				fmt.Sprintf("printf 'Server = %s\\n' \"$READ_TOKEN\" \\\n  | sudo tee %s > /dev/null\n", withCredentials(server), file),
			Note: "Create a read token on Your account › Tokens and enter it at the prompt; percent-encode an @ in your user name. Do this before pacman -Syu."})
	}
	return out
}

// ArtifactUsage returns the snippets of one package. p is a package folder of the pool (with
// version v, or "" for the newest) or a package file in one. The pinned mode installs the file of
// that version with pacman -U; the latest mode installs the package from the repository.
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	if from.Kind != repos.KindHosted {
		return nil, nil
	}
	p = strings.Trim(p, "/")
	var art format.Artifact
	var ver, file string
	if isPackageFile(p) {
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
		art = format.Artifact{Path: "pool/" + row.Name + "/", Title: row.Name, Name: row.Name, Kind: "package"}
		ver, file = row.Version, row.Filename
	} else {
		a, pkg, ok := packageOf(p)
		if !ok {
			return nil, nil
		}
		below, limit := top, int64(1)
		if v != "" {
			below = append(versionKey(v), 0)
		}
		rows, err := h.q.VersionsPage(ctx, from.ID, pkg, below, limit)
		if err != nil || len(rows) == 0 {
			return nil, err
		}
		vs := group(rows)
		if v != "" && vs[0].name != v {
			return nil, nil
		}
		art, ver, file = a, vs[0].name, vs[0].files[0].Filename
	}
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	out := h.pacmanSetup(repo, baseURL, format.SnippetArtifact)
	install := format.Snippet{ID: "pacman.install", Tool: "pacman", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
		Title: "Install " + art.Title, Lang: "sh", Code: "sudo pacman -Syu " + art.Title + "\n", Note: "pacman installs the newest version of the package."}
	if mode == format.VersionPinned {
		url := baseURL + "/arch/" + repo.Name + "/" + file
		local := "/tmp/" + baseName(file)
		get := fmt.Sprintf("curl -fsSL %s -o %s\ncurl -fsSL %s.sig -o %s.sig\n", url, local, url, local)
		if !repo.AnonymousRead {
			get = format.ReadTokenPrompt + "\n" + format.ReadTokenConfig + " > /tmp/curl-auth\n" +
				fmt.Sprintf("curl -fsSL -K /tmp/curl-auth %s -o %s\ncurl -fsSL -K /tmp/curl-auth %s.sig -o %s.sig\nrm /tmp/curl-auth\n", url, local, url, local)
		}
		install.Code = get + "sudo pacman -U " + local + "\n"
		install.Note = "pacman checks the package with the signature beside it."
	}
	out = append(out, install)
	return &format.ArtifactUsage{Artifact: art, Version: ver, Mode: mode, Snippets: out}, nil
}

// packageDetails is the answer of GET /api/arch/{name}/package: the fields of a package's index
// entry.
type packageDetails struct {
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Architecture string              `json:"architecture"`
	Fields       map[string][]string `json:"fields"`
}

// detailsOf returns the details of the package file p of repo, or nil when p is none.
func (h *Handler) detailsOf(ctx context.Context, repo *repos.Repo, p string) (*packageDetails, error) {
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
	return &packageDetails{Name: row.Name, Version: row.Version, Architecture: row.Arch, Fields: descFields(row.Desc)}, nil
}
