package docker

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// apiRoutes mounts the JSON routes of the web UI under /api/docker/.
func (f *Format) apiRoutes(r *route.Router) {
	r.Handle("GET /api/docker/{name}/manifest", route.RepoRead(), f.manifestDetails)
}

// descriptorJSON is a blob or manifest that a manifest names.
type descriptorJSON struct {
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

// platformJSON is a manifest of an index with its platform.
type platformJSON struct {
	Digest       string `json:"digest"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

// manifestJSON is the answer of GET /api/docker/{name}/manifest.
type manifestJSON struct {
	Digest       string           `json:"digest"`
	MediaType    string           `json:"media_type"`
	ArtifactType string           `json:"artifact_type"`
	Size         int64            `json:"size"`
	TotalSize    int64            `json:"total_size"`
	Config       *descriptorJSON  `json:"config"`
	Layers       []descriptorJSON `json:"layers"`
	Platforms    []platformJSON   `json:"platforms"`
	Subject      *descriptorJSON  `json:"subject"`
	Tags         []string         `json:"tags"`
	Referrers    int64            `json:"referrers"`
}

// maxDetailTags is how many tags the manifest details list.
const maxDetailTags = 100

// manifestDetails answers GET /api/docker/{name}/manifest?path=: the details of the manifest at a
// tag or manifest path of the repo (in a group, of the first readable member that stores it):
// its digest, types and sizes, its config and layers or its platform manifests, its subject, the
// first 100 tags that name it and how many manifests name it as their subject.
func (f *Format) manifestDetails(w http.ResponseWriter, r *http.Request, c *route.Call) {
	if c.Repo.Format != name {
		httpx.Error(w, r, http.StatusNotFound, "not a docker repository")
		return
	}
	p, err := repopath.Parse(strings.TrimPrefix(r.URL.Query().Get("path"), "/"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "path must be a tag or manifest path of the repository")
		return
	}
	op, ok := parsePath(string(p))
	if !ok || op.kind == kindLink {
		httpx.Error(w, r, http.StatusBadRequest, "path must be a tag or manifest path of the repository")
		return
	}
	ctx := r.Context()
	a, err := f.h.Reader.Stored(ctx, r, c.P, c.Repo, p)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if a == nil {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	m, err := f.readManifest(ctx, a)
	var oe *ociError
	if errors.As(err, &oe) {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "the file is not a manifest that the registry reads: "+oe.message)
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	out := manifestJSON{Digest: "sha256:" + a.SHA256, MediaType: m.MediaType, ArtifactType: m.artifactType(), Size: a.Size,
		TotalSize: m.TotalSize(), Layers: []descriptorJSON{}, Platforms: []platformJSON{}, Tags: []string{}}
	desc := func(d Descriptor) descriptorJSON {
		return descriptorJSON{Digest: d.Digest, MediaType: d.MediaType, Size: d.Size}
	}
	if m.Config != nil {
		d := desc(*m.Config)
		out.Config = &d
	}
	for _, l := range m.Layers {
		out.Layers = append(out.Layers, desc(l))
	}
	for _, ch := range m.Manifests {
		pj := platformJSON{Digest: ch.Digest}
		if ch.Platform != nil {
			pj.OS, pj.Architecture, pj.Variant = ch.Platform.OS, ch.Platform.Architecture, ch.Platform.Variant
		}
		out.Platforms = append(out.Platforms, pj)
	}
	if m.Subject != nil {
		d := desc(*m.Subject)
		out.Subject = &d
	}
	tags, err := f.q.SomeTagsOfDigest(ctx, dbq.SomeTagsOfDigestParams{RepoID: a.RepoID, Image: op.image, Digest: a.SHA256, Limit: maxDetailTags})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	out.Tags = append(out.Tags, tags...)
	if out.Referrers, err = f.q.CountReferrers(ctx, dbq.CountReferrersParams{RepoID: a.RepoID, Image: op.image, Subject: a.SHA256}); err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
