package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// headBytes is how much of a tarball a plan reads. npm pack writes package.json first.
const headBytes = 1 << 20

func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single:  true,
		Accept:  []string{".tgz"},
		Hint:    "One or more package tarballs (.tgz), as npm pack writes them.",
		Keep:    []string{"**/*.tgz"},
		One:     "package",
		Many:    "packages",
		Note:    "The server reads each package's name and version from its package.json, adds the version and moves the latest tag to it when it is the highest release.",
		Ignored: "Only .tgz package tarballs are uploaded.",
		Empty:   "The upload has no .tgz package tarball.",
		Reads:   []format.ReadRule{{Glob: "**/*.tgz", MaxBytes: headBytes}},
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// Plan plans a web UI upload: one item per .tgz, titled "<name>@<version>", that goes to its
// tarball path. A tarball without a valid package.json, or a version that exists, is a problem. A
// tarball whose package.json lies past the bytes the plan reads is checked at the upload.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, nil); err != nil {
		return nil, err
	}
	plan := &format.Plan{}
	for _, f := range in.Files {
		if !strings.HasSuffix(f.Name(), ".tgz") {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: f.Path, Files: []format.PlanTarget{{Path: f.Path}}}
		pj, err := readPackageJSON(bytes.NewReader(f.Head))
		var bad badTarball
		switch {
		case errors.As(err, &bad) && f.Truncated():
			// package.json is past the bytes read: the upload reads the whole file.
		case errors.As(err, &bad):
			item.Problems = append(item.Problems, bad.Error())
		case err != nil:
			return nil, err
		default:
			n, v, _, err := manifestOf(pj)
			if errors.As(err, &bad) {
				item.Problems = append(item.Problems, bad.Error())
				break
			}
			item.Title = n + "@" + v
			item.Facts = map[string]string{"name": n, "version": v}
			item.Files[0].Target = tarballPath(n, v)
			if _, err := h.q.GetVersionTarball(ctx, dbq.GetVersionTarballParams{RepoID: repo.ID, Name: n, Version: v}); err == nil {
				item.Problems = append(item.Problems, "version "+v+" of "+n+" already exists")
			} else if !isNoRows(err) {
				return nil, err
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// Store adds the version of each tarball of a plan.
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var out []*assets.Result
	for _, it := range plan.Items {
		for _, t := range it.Files {
			sp := files[t.Path]
			if sp == nil {
				return out, format.BadUpload("the upload has no file %s", t.Path)
			}
			res, err := h.ingest(ctx, repo, sp, nil, o)
			var ref *assets.Refusal
			if errors.As(err, &ref) {
				return out, assets.Refuse(ref.Status, "%s: %s", t.Path, ref.Message)
			}
			if err != nil {
				return out, err
			}
			out = append(out, res)
		}
	}
	return out, nil
}

// ingest adds the version of the tarball sp, read from its package.json: an upload, an import or
// the demo data. tags are the dist-tags that name it; nil moves latest to it when it is the
// highest release.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, sp *assets.Spooled, tags []string, o assets.Origin) (*assets.Result, error) {
	r, err := sp.Reader()
	if err != nil {
		return nil, err
	}
	pj, err := readPackageJSON(r)
	var bad badTarball
	if errors.As(err, &bad) {
		return nil, assets.Refuse(http.StatusBadRequest, "%s", bad.Error())
	}
	if err != nil {
		return nil, err
	}
	n, v, manifest, err := manifestOf(pj)
	if errors.As(err, &bad) {
		return nil, assets.Refuse(http.StatusBadRequest, "%s", bad.Error())
	}
	if err != nil {
		return nil, err
	}
	return h.addVersion(ctx, repo, &publishMeta{name: n, version: v, manifest: manifest, tags: tags, latestIfHigher: tags == nil}, sp, nil, o)
}

// importTarget is the Target of the Nexus import: a tarball <name>/-/<file>.tgz at its own path.
// Nexus's packuments are not imported: the server renders its own.
func importTarget(_ context.Context, _ *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	pkg, file, ok := splitFile(it.Path)
	if !ok || !strings.HasSuffix(file, ".tgz") {
		return "", false, nil
	}
	p, err := repopath.Parse(pkg + "/-/" + file)
	if err != nil {
		return "", false, nil
	}
	return p, true, nil
}

// importStore stores a tarball of a Nexus import with the dist-tags of its package at the source
// that name its version. Without them, latest is the highest release (refresh).
func (h *Handler) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	r, err := sp.Reader()
	if err != nil {
		return nil, err
	}
	pj, err := readPackageJSON(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", it.Path, err)
	}
	n, v, manifest, err := manifestOf(pj)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", it.Path, err)
	}
	tags := []string{}
	if it.Fetch != nil {
		if rc, _, err := it.Fetch(ctx, "-/package/"+n+"/dist-tags", http.Header{"Accept": {"application/json"}}); err == nil {
			var m map[string]string
			if json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&m) == nil {
				for t, tv := range m {
					if tv == v && validTag(t) {
						tags = append(tags, t)
					}
				}
			}
			rc.Close()
		} else {
			h.log.Warn("npm import: no dist-tags at the source", "package", n, "err", err)
		}
	}
	return h.addVersion(ctx, repo, &publishMeta{name: n, version: v, manifest: manifest, tags: tags}, sp, nil, it.Origin())
}
