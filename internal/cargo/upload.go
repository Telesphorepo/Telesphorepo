package cargo

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/cargo/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Web UI uploads and Nexus imports of .crate files: the index entry of each comes from the
// normalised Cargo.toml in the crate, as Cargo builds the publish request from it.

// headBytes is how much of a .crate a plan reads. Cargo.toml is one of the first files of the tar;
// a crate whose Cargo.toml lies past these bytes is checked when the whole file arrives.
const headBytes = 1 << 20

// ownIndex reports whether the index URL of a dependency (registry-index) is repo's own index, so
// the entry names no registry for it.
func ownIndex(repo *repos.Repo) func(string) bool {
	return func(reg string) bool {
		u := strings.TrimSuffix(strings.TrimPrefix(reg, "sparse+"), "/")
		return strings.HasSuffix(u, "/cargo/"+repo.Name+"/index")
	}
}

// entryOfCrate reads the index entry of the .crate of sp from its Cargo.toml. A crate that does
// not parse is a 400 refusal.
func entryOfCrate(repo *repos.Repo, sp *assets.Spooled) (*entry, error) {
	b, err := readManifest(io.NewSectionReader(readerAt(sp), 0, sp.Size))
	if err == nil {
		var e *entry
		if e, err = entryOfManifest(b, sp.SHA256, ownIndex(repo)); err == nil {
			return e, nil
		}
	}
	var bc badCrate
	if errors.As(err, &bc) {
		return nil, format.BadUpload("%s", bc.Error())
	}
	return nil, format.BadUpload("the .crate ended early")
}

func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Accept:  []string{".crate"},
		Hint:    "One or more .crate files, or a folder of them (cargo package writes them to target/package/).",
		Keep:    []string{"**/*.crate"},
		One:     "crate",
		Many:    "crates",
		Note:    "The server reads each crate's name, version and dependencies from its Cargo.toml and adds the version to the index.",
		Ignored: "Only .crate files are uploaded.",
		Empty:   "The upload has no .crate file.",
		Reads:   []format.ReadRule{{Glob: "**/*.crate", MaxBytes: headBytes}},
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// Plan plans a web UI upload: one item per .crate, titled "<name> <version>", that goes to
// crates/<name>/<name>-<version>.crate. A crate that does not parse, or whose version the repo
// has, is a problem. Other files are left out.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, nil); err != nil {
		return nil, err
	}
	plan := &format.Plan{}
	for _, f := range in.Files {
		if !strings.HasSuffix(f.Name(), ".crate") {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: f.Path, Files: []format.PlanTarget{{Path: f.Path}}}
		b, err := readManifest(bytes.NewReader(f.Head))
		var bc badCrate
		switch {
		case errors.Is(err, io.ErrUnexpectedEOF) && f.Truncated():
			// Cargo.toml is past the bytes read: the upload reads the whole file.
		case errors.As(err, &bc):
			item.Problems = append(item.Problems, bc.Error())
		case err != nil:
			item.Problems = append(item.Problems, "the .crate ended early")
		default:
			e, err := entryOfManifest(b, "", ownIndex(repo))
			if errors.As(err, &bc) {
				item.Problems = append(item.Problems, bc.Error())
				break
			} else if err != nil {
				return nil, fmt.Errorf("plan %s: %w", f.Path, err)
			}
			v, err := checkEntry(e)
			if err != nil {
				item.Problems = append(item.Problems, err.Error())
				break
			}
			item.Title = e.Name + " " + e.Vers
			item.Facts = map[string]string{"crate": e.Name, "version": e.Vers}
			item.Files[0].Target = cratePath(e.Name, e.Vers)
			if msg, err := h.clash(ctx, repo, e, v); err != nil {
				return nil, err
			} else if msg != "" {
				item.Problems = append(item.Problems, msg)
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// clash says why repo cannot take the crate version of e: its name folds to the name of another
// crate, or it has the version. "" when it can.
func (h *Handler) clash(ctx context.Context, repo *repos.Repo, e *entry, v version.SemVer) (string, error) {
	held, err := h.q.GetName(ctx, dbq.GetNameParams{RepoID: repo.ID, FoldedName: fold(e.Name)})
	switch {
	case err == nil && held != e.Name:
		return "the crate name clashes with the crate " + held + " of this repository", nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	row, err := h.q.GetVersionByBase(ctx, dbq.GetVersionByBaseParams{RepoID: repo.ID, LowerName: strings.ToLower(e.Name), BaseKey: baseKey(v)})
	switch {
	case err == nil:
		return "version " + row.Version + " is already uploaded", nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	return "", nil
}

// Store stores each crate of a plan with the entry of its Cargo.toml (ingest).
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var out []*assets.Result
	for _, it := range plan.Items {
		for _, t := range it.Files {
			sp := files[t.Path]
			if sp == nil {
				return out, format.BadUpload("the upload has no file %s", t.Path)
			}
			res, err := h.storeCrate(ctx, repo, sp, o)
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

// storeCrate stores the .crate of sp with the entry of its Cargo.toml.
func (h *Handler) storeCrate(ctx context.Context, repo *repos.Repo, sp *assets.Spooled, o assets.Origin) (*assets.Result, error) {
	e, err := entryOfCrate(repo, sp)
	if err != nil {
		return nil, err
	}
	return h.ingest(ctx, repo, e, sp, o)
}

// splitCrateFile splits "<name>-<version>.crate" at the first "-" after which a SemVer version
// follows.
func splitCrateFile(base string) (string, string, bool) {
	stem, ok := strings.CutSuffix(base, ".crate")
	if !ok {
		return "", "", false
	}
	for i := 0; i < len(stem); i++ {
		if stem[i] != '-' {
			continue
		}
		n, v := stem[:i], stem[i+1:]
		if _, err := version.ParseSemVer(v); err == nil && crateNameRE.MatchString(n) {
			return n, v, true
		}
	}
	return "", "", false
}

// importTarget is the path of a .crate of a source repo (format.Importer Target): a file
// <name>-<version>.crate, or a download path that ends in <name>/<version>/download or
// <name>/<version>. The path is the one the crate gets here when the source names it so; the
// store reads the true name and version from the crate. Every other file (an index file, the
// config.json) is ignored.
func importTarget(_ context.Context, _ *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	segs := strings.Split(strings.Trim(it.Path, "/"), "/")
	last := segs[len(segs)-1]
	if n, v, ok := splitCrateFile(last); ok {
		p, err := repopath.Parse(cratePath(n, v))
		return p, err == nil, nil
	}
	if strings.HasSuffix(last, ".crate") {
		p, err := repopath.Parse(strings.Trim(it.Path, "/"))
		return p, err == nil, nil
	}
	if last == "download" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) >= 2 {
		n, v := segs[len(segs)-2], segs[len(segs)-1]
		if _, err := version.ParseSemVer(v); err == nil && crateNameRE.MatchString(n) {
			p, err := repopath.Parse(cratePath(n, v))
			return p, err == nil, nil
		}
	}
	return "", false, nil
}

// importStore stores a .crate of a source repo with the entry of its Cargo.toml (format.Importer
// Store).
func (h *Handler) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	return h.storeCrate(ctx, repo, sp, it.Origin())
}
