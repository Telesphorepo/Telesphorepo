package docker

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Versions and builds (format.Spec Versions and Builds) of an image. An artifact is an image
// folder <image>/ that has a docker_images row; its versions are its tags, newest first, each a
// release with one build: the manifest that the tag names, with its config and layers (an index
// lists its platform manifests).

// Artifact kinds of an image.
const (
	kindImage    = "image"
	kindHelm     = "helm-chart"
	kindArtifact = "artifact"
)

// Config media types.
const (
	ctHelmConfig   = "application/vnd.cncf.helm.config.v1+json"
	ctDockerConfig = "application/vnd.docker.container.image.v1+json"
	ctOCIConfig    = "application/vnd.oci.image.config.v1+json"
)

// kindOf is the artifact kind of a manifest row: a Helm chart, a container image (an image config
// or an index), or another OCI artifact.
func kindOf(m dbq.DockerManifest) string {
	switch {
	case m.ConfigType == ctHelmConfig:
		return kindHelm
	case isIndex(m.MediaType), m.ConfigType == ctDockerConfig, m.ConfigType == ctOCIConfig:
		return kindImage
	}
	return kindArtifact
}

// imageOf returns the image of an artifact folder of repo and its artifact; ok is false when the
// folder is not an image.
func (f *Format) imageOf(ctx context.Context, repo *repos.Repo, folder string) (string, *format.Artifact, error) {
	image := strings.Trim(folder, "/")
	if image == "" {
		return "", nil, nil
	}
	if _, err := f.q.GetImage(ctx, dbq.GetImageParams{RepoID: repo.ID, Image: image}); errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	} else if err != nil {
		return "", nil, err
	}
	kind := kindImage
	rows, err := f.q.ListTagsRecent(ctx, dbq.ListTagsRecentParams{RepoID: repo.ID, Image: image, BeforeAt: math.MaxInt64, Limit: 1})
	if err != nil {
		return "", nil, err
	}
	if len(rows) > 0 {
		m, err := f.q.GetManifest(ctx, dbq.GetManifestParams{RepoID: repo.ID, Image: image, Digest: rows[0].Digest})
		switch {
		case err == nil:
			kind = kindOf(m)
		case !errors.Is(err, sql.ErrNoRows):
			return "", nil, err
		}
	}
	return image, &format.Artifact{Path: image + "/", Title: repo.Name + "/" + image, Name: path.Base(image), Kind: kind}, nil
}

// tagBuild returns the CI build record of the tag asset of image (nil: none).
func (f *Format) tagBuild(ctx context.Context, loader *ci.Loader, repo *repos.Repo, image, tag string) (*ci.Build, error) {
	a, err := f.h.Assets.Get(ctx, repo.ID, tagPath(image, tag))
	if errors.Is(err, assets.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loader.Get(ctx, a.BuildID)
}

// versions lists the tags of an image folder, newest first. after is the last tag of the previous
// page.
func (f *Format) versions(ctx context.Context, repo *repos.Repo, folder, after string, limit int) (*format.Artifact, []format.Version, string, error) {
	image, art, err := f.imageOf(ctx, repo, folder)
	if err != nil || art == nil {
		return nil, nil, "", err
	}
	beforeAt, beforeTag := int64(math.MaxInt64), ""
	if after != "" {
		t, err := f.q.GetTag(ctx, dbq.GetTagParams{RepoID: repo.ID, Image: image, Tag: after})
		if errors.Is(err, sql.ErrNoRows) {
			return art, []format.Version{}, "", nil
		}
		if err != nil {
			return nil, nil, "", err
		}
		beforeAt, beforeTag = t.UpdatedAt, t.Tag
	}
	rows, err := f.q.ListTagsRecent(ctx, dbq.ListTagsRecentParams{RepoID: repo.ID, Image: image, BeforeAt: beforeAt, BeforeTag: beforeTag, Limit: int64(limit)})
	if err != nil {
		return nil, nil, "", err
	}
	loader := f.h.Builds.Loader()
	out := make([]format.Version, 0, len(rows))
	for _, t := range rows {
		b, err := f.tagBuild(ctx, loader, repo, image, t.Tag)
		if err != nil {
			return nil, nil, "", err
		}
		out = append(out, format.Version{Version: t.Tag, Kind: format.VersionRelease, LatestBuild: shortDigest(t.Digest),
			BuildsCount: 1, PublishedAt: time.Unix(t.UpdatedAt, 0).UTC(), CI: b})
	}
	next := ""
	if len(rows) == limit && limit > 0 {
		next = rows[len(rows)-1].Tag
	}
	return art, out, next, nil
}

// shortDigest is the first 12 hex digits of a digest, as docker shows image IDs.
func shortDigest(hex string) string { return hex[:min(12, len(hex))] }

// builds lists the one build of tag of an image folder: the files of the manifest that the tag
// names.
func (f *Format) builds(ctx context.Context, repo *repos.Repo, folder, tag, build, after string, limit int) (*format.Artifact, *format.Version, []format.Build, string, error) {
	image, art, err := f.imageOf(ctx, repo, folder)
	if err != nil || art == nil {
		return nil, nil, nil, "", err
	}
	t, err := f.q.GetTag(ctx, dbq.GetTagParams{RepoID: repo.ID, Image: image, Tag: tag})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, "", nil
	}
	if err != nil {
		return nil, nil, nil, "", err
	}
	b, err := f.tagBuild(ctx, f.h.Builds.Loader(), repo, image, tag)
	if err != nil {
		return nil, nil, nil, "", err
	}
	at := time.Unix(t.UpdatedAt, 0).UTC()
	ver := &format.Version{Version: tag, Kind: format.VersionRelease, LatestBuild: shortDigest(t.Digest), BuildsCount: 1, PublishedAt: at, CI: b}
	if after != "" || limit <= 0 || build != "" && build != ver.LatestBuild {
		return art, ver, []format.Build{}, "", nil
	}
	files, err := f.manifestFiles(ctx, repo, image, t.Digest)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return art, ver, []format.Build{{Build: shortDigest(t.Digest), Files: files, PublishedAt: at, CI: b}}, "", nil
}

// manifestFiles returns the files of the manifest digest of image: the manifest, then the config
// and the layers of an image manifest, or the platform manifests of an index.
func (f *Format) manifestFiles(ctx context.Context, repo *repos.Repo, image, digest string) ([]format.BuildFile, error) {
	mp := manifestPath(image, digest)
	a, err := f.h.Assets.Get(ctx, repo.ID, mp)
	if errors.Is(err, assets.ErrNotFound) {
		return []format.BuildFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []format.BuildFile{{Name: "sha256:" + shortDigest(digest), Path: mp, Size: a.Size, SHA256: a.SHA256, Extension: "manifest"}}
	m, err := f.readManifest(ctx, a)
	if err != nil {
		return nil, err
	}
	file := func(d Descriptor, p, classifier, ext string) format.BuildFile {
		return format.BuildFile{Name: "sha256:" + shortDigest(d.hex), Path: p, Size: d.Size, SHA256: d.hex, Classifier: classifier, Extension: ext}
	}
	if m.Config != nil {
		out = append(out, file(*m.Config, linkPath(image, m.Config.hex), "", "config"))
	}
	for _, l := range m.Layers {
		if l.hex != "" {
			out = append(out, file(l, linkPath(image, l.hex), "", "layer"))
		}
	}
	for _, c := range m.Manifests {
		out = append(out, file(c, manifestPath(image, c.hex), platformOf(c.Platform), "manifest"))
	}
	return out, nil
}

// platformOf is "<os>/<arch>[/<variant>]", "unknown/unknown" without a platform.
func platformOf(p *Platform) string {
	if p == nil || p.OS == "" {
		return "unknown/unknown"
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// readManifest reads and parses the manifest of asset a (at most 4 MiB).
func (f *Format) readManifest(ctx context.Context, a *assets.Asset) (*Manifest, error) {
	b, err := f.h.Assets.ReadAll(ctx, a, maxManifest)
	if err != nil {
		return nil, err
	}
	return ParseManifest(a.ContentType, b)
}
