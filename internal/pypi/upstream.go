package pypi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The page of a proxy: the upstream page of the project, cached at .simple/<name> in the JSON form
// (PEP 691) or the HTML form (PEP 503) that the remote sent. Each file URL, resolved against the
// upstream page's URL, maps to a local path: under the files base of the repo config to
// files/<rest>, under the remote URL to remote/<rest>. A file at any other address is left out,
// so the server never fetches an address that a remote document names.

// maxPage is the largest upstream page the server reads.
const maxPage = 64 << 20

// upstreamFile is a file of an upstream JSON page.
type upstreamFile struct {
	Filename         string            `json:"filename"`
	URL              string            `json:"url"`
	Hashes           map[string]string `json:"hashes"`
	RequiresPython   *string           `json:"requires-python"`
	Yanked           json.RawMessage   `json:"yanked"`
	Size             *int64            `json:"size"`
	UploadTime       string            `json:"upload-time"`
	CoreMetadata     json.RawMessage   `json:"core-metadata"`
	DistInfoMetadata json.RawMessage   `json:"dist-info-metadata"`
}

// proxyModel builds the page of project n of proxy repo from its cached upstream page a.
func (h *Handler) proxyModel(ctx context.Context, repo *repos.Repo, n string, a *assets.Asset) (*model, error) {
	body, err := h.assets.ReadAll(ctx, a, maxPage)
	if err != nil {
		return nil, fmt.Errorf("read the upstream page of %s: %w", n, err)
	}
	remote := h.remoteURL(repo)
	pageURL, err := url.Parse(remote + "simple/" + url.PathEscape(n) + "/")
	if err != nil {
		return nil, err
	}
	bases := []localBase{{"files/", filesBase(repo)}, {"remote/", remote}}
	var files []upstreamFile
	var versions []string
	ct, _, _ := mime.ParseMediaType(a.ContentType)
	if ct == ctJSON || strings.HasSuffix(ct, "+json") || ct == "application/json" || bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var page struct {
			Versions []string       `json:"versions"`
			Files    []upstreamFile `json:"files"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("the upstream page of %s is not valid JSON: %w", n, err)
		}
		files, versions = page.Files, page.Versions
	} else {
		files = parseHTMLPage(body)
	}
	m := &model{name: n}
	seen := map[string]bool{}
	for _, f := range files {
		ref, err := url.Parse(f.URL)
		if err != nil || f.Filename == "" {
			continue
		}
		u := pageURL.ResolveReference(ref)
		u.Fragment, u.RawFragment = "", ""
		local, ok := localPath(u, bases)
		if !ok {
			continue
		}
		x := mfile{filename: f.Filename, href: "../../" + escapePath(local), hashes: f.Hashes, size: -1}
		if f.Size != nil {
			x.size = *f.Size
		}
		if f.RequiresPython != nil {
			x.requiresPython = *f.RequiresPython
		}
		if t, err := time.Parse(time.RFC3339Nano, f.UploadTime); err == nil {
			x.uploadTime = t
		}
		cm := f.CoreMetadata
		if len(cm) == 0 || string(cm) == "null" {
			cm = f.DistInfoMetadata
		}
		x.metadata, x.metaSHA = metadataOf(cm)
		x.yanked = yankedOf(f.Yanked)
		m.files = append(m.files, x)
		if len(versions) == 0 {
			if d, err := parseFilename(f.Filename); err == nil && !seen[d.version.String()] {
				seen[d.version.String()] = true
				m.versions = append(m.versions, d.version.String())
			}
		}
	}
	if len(versions) > 0 {
		m.versions = versions
	}
	return m, nil
}

// localBase is a base URL whose files a proxy keeps under prefix.
type localBase struct {
	prefix, base string
}

// localPath maps the file URL u to its local path: prefix and the part of the path under the
// first base of bases with the scheme and host of u.
func localPath(u *url.URL, bases []localBase) (string, bool) {
	for _, b := range bases {
		bu, err := url.Parse(b.base)
		if err != nil || b.base == "" || !strings.EqualFold(bu.Scheme, u.Scheme) || !strings.EqualFold(bu.Host, u.Host) {
			continue
		}
		rest, ok := strings.CutPrefix(u.Path, bu.Path)
		if !ok || rest == "" || u.RawQuery != "" {
			continue
		}
		p, err := repopath.Parse(b.prefix + rest)
		if err != nil {
			continue
		}
		return string(p), true
	}
	return "", false
}

// escapePath escapes each segment of p for a URL.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// metadataOf reads a core-metadata value: true, false, or a map of hashes.
func metadataOf(raw json.RawMessage) (bool, string) {
	if len(raw) == 0 {
		return false, ""
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, ""
	}
	var hashes map[string]string
	if json.Unmarshal(raw, &hashes) == nil && hashes != nil {
		return true, hashes["sha256"]
	}
	return false, ""
}

// yankedOf reads a yanked value: nil when the file is not yanked, true, or the reason.
func yankedOf(raw json.RawMessage) any {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if b {
			return true
		}
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return nil
}

var (
	anchorRE = regexp.MustCompile(`(?is)<a\s([^>]*)>(.*?)</a\s*>`)
	attrRE   = regexp.MustCompile("(?s)([a-zA-Z_:][-a-zA-Z0-9_:.]*)(?:\\s*=\\s*(?:\"([^\"]*)\"|'([^']*)'|([^\\s\"'=<>`]+)))?")
	tagRE    = regexp.MustCompile(`(?s)<[^>]*>`)
)

// parseHTMLPage reads the anchors of an HTML project page (PEP 503) as files: the href (its
// #<hash>=<hex> fragment gives a hash), the text as the file name, and the data- attributes.
func parseHTMLPage(body []byte) []upstreamFile {
	var out []upstreamFile
	for _, m := range anchorRE.FindAllSubmatch(body, -1) {
		attrs := map[string]*string{}
		for _, a := range attrRE.FindAllSubmatch(m[1], -1) {
			v := html.UnescapeString(string(a[2]) + string(a[3]) + string(a[4]))
			attrs[strings.ToLower(string(a[1]))] = &v
		}
		href := attrs["href"]
		if href == nil {
			continue
		}
		f := upstreamFile{Filename: strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(string(m[2]), ""))), URL: *href}
		if _, frag, ok := strings.Cut(*href, "#"); ok {
			if alg, hex, ok := strings.Cut(frag, "="); ok {
				f.Hashes = map[string]string{alg: hex}
			}
		}
		f.RequiresPython = attrs["data-requires-python"]
		cm := attrs["data-core-metadata"]
		if cm == nil {
			cm = attrs["data-dist-info-metadata"]
		}
		if cm != nil {
			switch alg, hex, ok := strings.Cut(*cm, "="); {
			case ok && alg == "sha256":
				f.CoreMetadata, _ = json.Marshal(map[string]string{"sha256": hex})
			case *cm == "true" || ok:
				f.CoreMetadata = json.RawMessage("true")
			}
		}
		if y := attrs["data-yanked"]; y != nil {
			if *y == "" {
				f.Yanked = json.RawMessage("true")
			} else {
				f.Yanked, _ = json.Marshal(*y)
			}
		}
		out = append(out, f)
	}
	return out
}
