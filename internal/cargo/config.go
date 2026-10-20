package cargo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The repo config of Cargo repos: {"download": "<template>"}, used by proxies only. The template
// is the dl of the remote's config.json (Cargo book, Registry Index): the URL of a .crate with
// the markers {crate}, {version}, {prefix} and {lowerprefix}. A template without a marker gets
// "/{crate}/{version}/download" appended, as Cargo does. It is an admin's setting: the proxy
// never reads a URL from a remote document.

// defaultDownload is where crates.io serves its crates.
const defaultDownload = "https://static.crates.io/crates/{crate}/{crate}-{version}.crate"

var markers = []string{"{crate}", "{version}", "{prefix}", "{lowerprefix}", "{sha256-checksum}"}

// config is the repo config of a Cargo repo.
type config struct {
	Download string `json:"download,omitempty"`
}

// validateConfig accepts a JSON object with an optional download template.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("cargo config must be a JSON object")
	}
	for k := range m {
		if k != "download" {
			return nil, errors.New(`unknown cargo config option "` + k + `"; the option is download`)
		}
	}
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("download must be a string")
	}
	c.Download = strings.TrimSpace(c.Download)
	if c.Download != "" {
		t, err := checkDownload(c.Download)
		if err != nil {
			return nil, err
		}
		c.Download = t
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(b.Bytes())), nil
}

// checkDownload checks a download template and returns it with the markers Cargo appends to one
// that has none.
func checkDownload(t string) (string, error) {
	if strings.Contains(t, "{sha256-checksum}") {
		return "", errors.New("download: the proxy fetches a crate before it knows its checksum, so {sha256-checksum} cannot be used; use {crate} and {version}")
	}
	has := false
	for _, m := range markers {
		has = has || strings.Contains(t, m)
	}
	if !has {
		t = strings.TrimSuffix(t, "/") + "/{crate}/{version}/download"
	}
	sample := expand(t, "crate", "1.0.0")
	u, err := url.Parse(sample)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("download must be an http or https URL with a host and without a user, query or fragment, e.g. " + defaultDownload)
	}
	return t, nil
}

// expand fills the markers of template t for a version of a crate.
func expand(t, name, vers string) string {
	p := prefixOf(name)
	return strings.NewReplacer("{crate}", name, "{version}", vers, "{prefix}", p, "{lowerprefix}", strings.ToLower(p)).Replace(t)
}

// downloadOf returns the download template of a repo config: the configured one, else crates.io.
func downloadOf(raw json.RawMessage) string {
	var c config
	if json.Unmarshal(raw, &c) == nil && c.Download != "" {
		return c.Download
	}
	return defaultDownload
}

// fileSource splits the URL of a crate into the base URL of its host and the path under it, the
// Files and Remote of an assets.Class.
func fileSource(template, name, vers string) (files, remote string, ok bool) {
	u, err := url.Parse(expand(template, name, vers))
	if err != nil {
		return "", "", false
	}
	base, err := proxy.FilesURL(u.Scheme + "://" + u.Host + "/")
	if err != nil {
		return "", "", false
	}
	return base, strings.TrimPrefix(u.Path, "/"), true
}

// downloads is the download template of each Cargo proxy repo, by repo ID, read once from the repos
// and then kept by their changes. Classify gets the remote of a proxy, not the repo: it finds the
// repo whose remote is that one.
type downloads struct {
	rs   *repos.Service
	once sync.Once
	mu   sync.RWMutex
	by   map[int64]string
}

func (d *downloads) load(ctx context.Context) {
	d.once.Do(func() {
		all, err := d.rs.List(ctx)
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.by == nil {
			d.by = map[int64]string{}
		}
		if err != nil {
			return
		}
		for _, r := range all {
			if r.Format == name && r.Kind == repos.KindProxy {
				if _, known := d.by[r.ID]; !known {
					d.by[r.ID] = downloadOf(r.Config)
				}
			}
		}
	})
}

// changed keeps the templates of a change of repos (repos.Service.AfterChange).
func (d *downloads) changed(_ context.Context, n repos.Notice) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.by == nil {
		d.by = map[int64]string{}
	}
	for _, r := range n.Changed {
		if r.Format == name && r.Kind == repos.KindProxy {
			d.by[r.ID] = downloadOf(r.Config)
		}
	}
	if n.Deleted != nil {
		delete(d.by, n.Deleted.ID)
	}
}

// of returns the download template of the proxy whose remote is px.
func (d *downloads) of(px *repos.Proxy) string {
	d.load(context.Background())
	d.mu.RLock()
	defer d.mu.RUnlock()
	for id, t := range d.by {
		if p, ok := d.rs.Proxy(id); ok && p == px {
			return t
		}
	}
	return defaultDownload
}
