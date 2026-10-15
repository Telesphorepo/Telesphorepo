package maven

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"hash"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Proxy and group repos (spec "Proxy repositories", "Group repositories").

// Classify implements format.Proxied: maven-metadata.xml and its checksums, and the files of a
// -SNAPSHOT version, change at the remote; every other file is fetched once.
func (h *Handler) Classify(p string) (format.ProxyClass, bool) {
	if mutable(p) {
		return format.ProxyChanging, true
	}
	return format.ProxyImmutable, true
}

// ProxyProbe implements format.Proxied: a check of the remote requests its root.
func (h *Handler) ProxyProbe(json.RawMessage) string { return "" }

// ServesGroups implements format.Grouper.
func (h *Handler) ServesGroups() {}

// assetKind is the assets kind of a Maven path.
func assetKind(p string) string {
	if strings.HasPrefix(path.Base(p), "maven-metadata.xml") {
		return assets.KindMetadata
	}
	return assets.KindArtifact
}

// serveProxy answers a read of a proxy repo.
func (h *Handler) serveProxy(w http.ResponseWriter, r *http.Request, repo *repos.Repo, p string) {
	c, _ := h.Classify(p)
	h.host.Proxy.Serve(w, r, repo, p, c == format.ProxyChanging, assetKind(p))
}

// has reports whether member repo m holds p, after a fetch for a proxy. A proxy whose remote
// fails with nothing cached gives proxy.ErrRemote.
func (h *Handler) has(ctx context.Context, m *repos.Repo, p string) (*assets.Asset, error) {
	var a *assets.Asset
	var err error
	if m.Kind == repos.KindProxy {
		c, _ := h.Classify(p)
		a, err = h.host.Proxy.Ensure(ctx, m, p, c == format.ProxyChanging, assetKind(p))
		if errors.Is(err, proxy.ErrNotFound) {
			return nil, nil
		}
	} else {
		a, err = h.assets.Get(ctx, m.ID, p)
		if errors.Is(err, assets.ErrNotFound) {
			return nil, nil
		}
	}
	return a, err
}

// checksumExts are the checksum files that a merged maven-metadata.xml has.
var checksumExts = map[string]func() hash.Hash{".md5": md5.New, ".sha1": sha1.New, ".sha256": sha256.New, ".sha512": sha512.New}

// mergedPath reports whether p is an artifact-level (or group-level) maven-metadata.xml or one
// of its checksums, which a group merges, and returns the metadata path and the checksum
// extension ("" for the file itself). A version-level file (under a -SNAPSHOT folder) is not
// merged: it comes from the first member that has it.
func mergedPath(p string) (md, ext string, ok bool) {
	base := path.Base(p)
	rest, found := strings.CutPrefix(base, "maven-metadata.xml")
	if !found || (rest != "" && checksumExts[rest] == nil) {
		return "", "", false
	}
	if strings.HasSuffix(path.Base(path.Dir(p)), "-SNAPSHOT") {
		return "", "", false
	}
	return strings.TrimSuffix(p, rest), rest, true
}

// serveGroup answers a read of group repo: the first member that the reader can read and that
// has p serves it; an artifact-level maven-metadata.xml merges over those members.
func (h *Handler) serveGroup(w http.ResponseWriter, r *http.Request, group *repos.Repo, pr *auth.Principal, p string) {
	ctx := r.Context()
	members, err := h.host.Repos.MemberRepos(ctx, group)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	readable := members[:0]
	for _, m := range members {
		ok, err := h.auth.ReadsThrough(ctx, r, pr, m)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		if ok {
			readable = append(readable, m)
		}
	}
	if md, ext, ok := mergedPath(p); ok {
		h.serveMerged(w, r, readable, p, md, ext)
		return
	}
	remoteFailed := false
	for _, m := range readable {
		a, err := h.has(ctx, m, p)
		switch {
		case errors.Is(err, proxy.ErrRemote):
			remoteFailed = true
		case err != nil:
			h.host.Log.Error("group read failed", "group", group.Name, "member", m.Name, "path", p, "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		case a != nil:
			h.assets.Serve(w, r, m, p)
			return
		}
	}
	notInGroup(w, remoteFailed)
}

func notInGroup(w http.ResponseWriter, remoteFailed bool) {
	if remoteFailed {
		httpx.Error(w, http.StatusBadGateway, "The remote repository did not answer.")
		return
	}
	httpx.Error(w, http.StatusNotFound, "not found")
}

// groupMetadata is the part of a maven-metadata.xml that a group merges: the versions of an
// artifact, or the plugins of a group id.
type groupMetadata struct {
	XMLName    xml.Name         `xml:"metadata"`
	GroupID    string           `xml:"groupId,omitempty"`
	ArtifactID string           `xml:"artifactId,omitempty"`
	Versioning *groupVersioning `xml:"versioning"`
	Plugins    *groupPlugins    `xml:"plugins"` // nil: no plugins element
}

type groupPlugins struct {
	Plugin []groupPlugin `xml:"plugin"`
}

type groupVersioning struct {
	Latest      string   `xml:"latest,omitempty"`
	Release     string   `xml:"release,omitempty"`
	Versions    []string `xml:"versions>version"`
	LastUpdated string   `xml:"lastUpdated,omitempty"`
}

type groupPlugin struct {
	Name       string `xml:"name,omitempty"`
	Prefix     string `xml:"prefix"`
	ArtifactID string `xml:"artifactId"`
}

// newer returns the newer of two versions ("" is older than any).
func newer(a, b string) string {
	if a == "" || (b != "" && compareVersions(b, a) > 0) {
		return b
	}
	return a
}

// mergeMetadata merges the metadata of the members, as a Nexus group does: the union of the
// versions in Maven order; latest is the highest version and release the highest version that is
// not a -SNAPSHOT (also when no member names them); the newest lastUpdated; the union of the
// plugins (no plugins element when there are none).
func mergeMetadata(all []groupMetadata) groupMetadata {
	var out groupMetadata
	var versions []string
	for _, m := range all {
		if out.GroupID == "" {
			out.GroupID = m.GroupID
		}
		if out.ArtifactID == "" {
			out.ArtifactID = m.ArtifactID
		}
		if v := m.Versioning; v != nil {
			if out.Versioning == nil {
				out.Versioning = &groupVersioning{}
			}
			o := out.Versioning
			o.Latest, o.Release = newer(o.Latest, strings.TrimSpace(v.Latest)), newer(o.Release, strings.TrimSpace(v.Release))
			if lu := strings.TrimSpace(v.LastUpdated); lu > o.LastUpdated {
				o.LastUpdated = lu
			}
			for _, x := range v.Versions {
				if x = strings.TrimSpace(x); x != "" {
					versions = append(versions, x)
				}
			}
		}
		if m.Plugins == nil {
			continue
		}
		for _, pl := range m.Plugins.Plugin {
			if out.Plugins == nil {
				out.Plugins = &groupPlugins{}
			}
			if !slices.ContainsFunc(out.Plugins.Plugin, func(x groupPlugin) bool { return x.Prefix == pl.Prefix }) {
				out.Plugins.Plugin = append(out.Plugins.Plugin, pl)
			}
		}
	}
	if out.Versioning != nil {
		slices.SortFunc(versions, compareVersions)
		o := out.Versioning
		o.Versions = slices.Compact(versions)
		for _, v := range o.Versions {
			o.Latest = newer(o.Latest, v)
			if !strings.HasSuffix(v, "-SNAPSHOT") {
				o.Release = newer(o.Release, v)
			}
		}
	}
	return out
}

// serveMerged answers maven-metadata.xml path md (or its checksum ext) of a group. With one
// member that has it, that member serves its own files; with more, the server merges them and
// computes the checksums from the merged bytes.
func (h *Handler) serveMerged(w http.ResponseWriter, r *http.Request, members []*repos.Repo, p, md, ext string) {
	ctx := r.Context()
	type source struct {
		repo *repos.Repo
		a    *assets.Asset
	}
	var srcs []source
	remoteFailed := false
	for _, m := range members {
		a, err := h.has(ctx, m, md)
		switch {
		case errors.Is(err, proxy.ErrRemote):
			remoteFailed = true
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		case a != nil:
			srcs = append(srcs, source{m, a})
		}
	}
	switch len(srcs) {
	case 0:
		notInGroup(w, remoteFailed)
		return
	case 1:
		if ext == "" {
			h.assets.Serve(w, r, srcs[0].repo, md)
			return
		}
		if a, err := h.has(ctx, srcs[0].repo, p); err == nil && a != nil {
			h.assets.Serve(w, r, srcs[0].repo, p)
			return
		}
	}
	var all []groupMetadata
	var modified time.Time
	for _, s := range srcs {
		if s.a.Size > maxMetadataBytes {
			continue
		}
		body, _, err := h.assets.Store().Get(ctx, assets.Key(s.repo.Name, md))
		if err != nil {
			h.host.Log.Warn("group: read member metadata", "member", s.repo.Name, "path", md, "err", err)
			continue
		}
		var m groupMetadata
		err = xml.NewDecoder(io.LimitReader(body, maxMetadataBytes)).Decode(&m)
		body.Close()
		if err != nil {
			h.host.Log.Warn("group: member metadata is not valid XML", "member", s.repo.Name, "path", md)
			continue
		}
		all = append(all, m)
		if s.a.UpdatedAt.After(modified) {
			modified = s.a.UpdatedAt
		}
	}
	if len(all) == 0 {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	merged := mergeMetadata(all)
	x, err := xml.MarshalIndent(merged, "", "  ")
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	data := append([]byte(xml.Header), append(x, '\n')...)
	ct := assets.ContentType(md)
	if ext != "" {
		sum := checksumExts[ext]()
		sum.Write(data)
		data, ct = []byte(hex.EncodeToString(sum.Sum(nil))), "text/plain"
	}
	etag := sha256.Sum256(data)
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if ct != "text/plain" {
		hd.Set("Content-Disposition", "attachment")
	}
	hd.Set("Content-Type", ct)
	hd.Set("ETag", `"`+hex.EncodeToString(etag[:])+`"`)
	http.ServeContent(w, r, "", modified, bytes.NewReader(data))
}
