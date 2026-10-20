package pypi

import (
	"context"
	"errors"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// uploadSpec is the web UI upload of PyPI repos: wheels and sdists, one by one or the dist/
// folder of a build.
func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Accept:  []string{".whl", ".tar.gz", ".zip"},
		Hint:    "Wheels (.whl) and source distributions (.tar.gz), or the dist folder of a build.",
		Keep:    []string{"**/*.whl", "**/*.tar.gz", "**/*.zip"},
		One:     "file",
		Many:    "files",
		Note:    "The server reads the project and the version from each file's metadata and lists the file on the project's page.",
		Ignored: "Only wheels and source distributions are uploaded.",
		Empty:   "The upload has no wheel or source distribution.",
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// Plan plans a web UI upload: one item per wheel or sdist, titled "<name> <version>", that goes
// to packages/<project>/<file name>. A file name that does not parse is a problem; the metadata in
// the file is checked at the upload. Other files are left out.
func (h *Handler) Plan(_ context.Context, _ *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, nil); err != nil {
		return nil, err
	}
	plan := &format.Plan{}
	for _, f := range in.Files {
		n := f.Name()
		if !strings.HasSuffix(n, ".whl") && !strings.HasSuffix(n, ".tar.gz") && !strings.HasSuffix(n, ".zip") {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: n, Files: []format.PlanTarget{{Path: f.Path}}}
		d, err := parseFilename(n)
		if err != nil {
			item.Problems = append(item.Problems, err.Error())
		} else {
			item.Title = d.name + " " + d.version.String()
			item.Facts = map[string]string{"project": normalize(d.name), "version": d.version.String(), "file": d.kind}
			item.Files[0].Target = "packages/" + normalize(d.name) + "/" + n
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// Store ingests each file of a plan.
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var out []*assets.Result
	for _, it := range plan.Items {
		for _, t := range it.Files {
			sp := files[t.Path]
			if sp == nil {
				return out, format.BadUpload("the upload has no file %s", t.Path)
			}
			name := t.Path[strings.LastIndexByte(t.Path, '/')+1:]
			rs, _, err := h.ingest(ctx, repo, name, sp, o, wantMeta{})
			var ref *assets.Refusal
			var bd badDist
			switch {
			case errors.As(err, &ref):
				return out, assets.Refuse(ref.Status, "%s: %s", t.Path, ref.Message)
			case errors.As(err, &bd):
				return out, format.BadUpload("%s: %s", t.Path, bd.Error())
			case err != nil:
				return out, err
			}
			out = append(out, rs...)
		}
	}
	return out, nil
}
