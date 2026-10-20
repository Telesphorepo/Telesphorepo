package nuget

import (
	"bytes"
	"context"
	"errors"
	"path"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// headBytes is how much of a .nupkg a plan reads. A zip archive keeps its directory at its end,
// so a plan reads a package whole up to this size; a larger one is read when the upload arrives.
const headBytes = 16 << 20

func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Accept:  []string{".nupkg"},
		Hint:    "One or more .nupkg packages, or a folder of them.",
		Keep:    []string{"**/*.nupkg"},
		One:     "package",
		Many:    "packages",
		Note:    "The server reads each package's id and version from its .nuspec and stores the package unchanged.",
		Ignored: "Only .nupkg packages are uploaded; symbol packages (.snupkg) are not.",
		Empty:   "The upload has no .nupkg package.",
		Reads:   []format.ReadRule{{Glob: "**/*.nupkg", MaxBytes: headBytes}},
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// Plan plans a web UI upload: one item per .nupkg, titled "<id> <version>", that goes to its
// flat-container path. A package that does not read, or a version the repo has, is a problem. A
// package larger than the plan reads is checked at the upload. Other files are left out.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, nil); err != nil {
		return nil, err
	}
	plan := &format.Plan{}
	for _, f := range in.Files {
		if !strings.HasSuffix(strings.ToLower(f.Name()), ".nupkg") || strings.HasSuffix(strings.ToLower(f.Name()), ".symbols.nupkg") {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: f.Path, Files: []format.PlanTarget{{Path: f.Path}}}
		if f.Truncated() {
			plan.Items = append(plan.Items, item)
			continue
		}
		info, err := parseNupkg(bytes.NewReader(f.Head), int64(len(f.Head)))
		var b badPackage
		switch {
		case errors.As(err, &b):
			item.Problems = append(item.Problems, b.msg)
		case err != nil:
			return nil, err
		default:
			v := info.Version.Normalized()
			item.Title = info.ID + " " + v
			item.Facts = map[string]string{"id": info.ID, "version": v}
			item.Files[0].Target, _ = packagePaths(info.lowerID(), info.lowerVersion())
			if r, err := h.getVersion(ctx, repo.ID, info.lowerID(), v); err != nil {
				return nil, err
			} else if r != nil {
				item.Problems = append(item.Problems, "version "+v+" of "+info.ID+" exists in this repository")
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// Store pushes each package of a plan (ingest).
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var out []*assets.Result
	for _, it := range plan.Items {
		for _, t := range it.Files {
			sp := files[t.Path]
			if sp == nil {
				return out, format.BadUpload("the upload has no file %s", t.Path)
			}
			res, info, err := h.ingest(ctx, repo, sp, o)
			var ref *assets.Refusal
			if errors.As(err, &ref) {
				return out, assets.Refuse(ref.Status, "%s: %s", t.Path, ref.Message)
			}
			if err != nil {
				return out, err
			}
			if res.Status == assets.Unchanged {
				return out, assets.Refuse(409, "%s: version %s of %s exists in this repository", t.Path, info.Version.Normalized(), info.ID)
			}
			out = append(out, res)
		}
	}
	return out, nil
}

// importTarget is the flat-container path of a .nupkg of a Nexus nuget repo: a source path
// <id>/<version> (an asset of a Nexus nuget hosted repo), or <id>/<version>/<id>.<version>.nupkg.
// Every other file is ignored: the server builds the documents itself.
func importTarget(_ context.Context, _ *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	segs := strings.Split(strings.Trim(it.Path, "/"), "/")
	var id, ver string
	switch {
	case len(segs) == 2:
		id, ver = segs[0], segs[1]
	case len(segs) == 3 && strings.HasSuffix(strings.ToLower(segs[2]), ".nupkg"):
		id, ver = segs[0], segs[1]
		if !strings.EqualFold(path.Base(segs[2]), id+"."+ver+".nupkg") {
			return "", false, nil
		}
	default:
		return "", false, nil
	}
	v, err := version.ParseNuGet(ver)
	if !validID(id) || err != nil {
		return "", false, nil
	}
	p, _ := packagePaths(strings.ToLower(id), strings.ToLower(v.Normalized()))
	rp, err := repopath.Parse(p)
	if err != nil {
		return "", false, errors.New("package id or version too long")
	}
	return rp, true, nil
}

// importStore stores a downloaded package through the push (ingest), with the source's dates.
func (h *Handler) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	res, _, err := h.ingest(ctx, repo, sp, it.Origin())
	return res, err
}
