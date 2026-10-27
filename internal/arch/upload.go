package arch

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/arch/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

const pkgContentType = "application/vnd.archlinux.package"

// generated is the origin of the files the server makes: signatures and indexes.
var generated = assets.Origin{Source: assets.SourceGenerated}

type uploadResult struct {
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"`
}

// pkgMeta is the Meta of the write of a package: its parse and its body.
type pkgMeta struct {
	info *pkgInfo
	sp   *assets.Spooled
}

// sigMeta is the Meta of the signature that an upload writes beside its package.
type sigMeta struct{}

// upload adds the package of the request to the repo: the body, or with
// ?upload=<id>&sha256=<hex>[&size=<n>] and no body the file of a resumable upload of the same
// publish session (/api/repos/{name}/uploads), which this request finishes.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var sp *assets.Spooled
	if id := r.URL.Query().Get("upload"); id != "" {
		if sp = h.finishUpload(w, r, c, id); sp == nil {
			return
		}
	} else {
		var ok bool
		if sp, ok = (format.Kit{Assets: h.assets}).SpoolBody(w, r); !ok {
			return
		}
	}
	defer sp.Close()
	res, out, err := h.ingest(r.Context(), c.Repo, sp, format.OriginOf(c.P, assets.SourcePut))
	switch {
	case err != nil:
		route.Fail(w, r, err)
	case res.Status != assets.Unchanged:
		httpx.WriteJSON(w, http.StatusCreated, out)
	default:
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// finishUpload ends the resumable upload id of the caller's publish session with the sha256 and
// size of the query, and returns its file. It answers the error itself and returns nil: 404 for
// an unknown upload, 400 for a bad query or bytes that do not match (the upload is deleted).
func (h *Handler) finishUpload(w http.ResponseWriter, r *http.Request, c *route.Call, id string) *assets.Spooled {
	q := r.URL.Query()
	digest := strings.ToLower(q.Get("sha256"))
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		httpx.Error(w, r, http.StatusBadRequest, "give sha256, the 64 hex digits of the SHA-256 of the whole file")
		return nil
	}
	size := int64(-1)
	if s := q.Get("size"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid size")
			return nil
		}
		size = n
	}
	sp, err := h.host.Uploads.Finish(r.Context(), id, c.P, c.Repo, digest, size)
	switch {
	case errors.Is(err, assets.ErrUploadUnknown):
		httpx.Error(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, assets.ErrDigest):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	case err != nil:
		route.Fail(w, r, err)
	default:
		return sp
	}
	return nil
}

// bodyOf returns the content of sp.
func bodyOf(sp *assets.Spooled) io.Reader {
	if sp.File != nil {
		return io.NewSectionReader(sp.File, 0, sp.Size)
	}
	return bytes.NewReader(sp.Data)
}

// signer returns the signer of repo's key, or nil (logged) when the key is missing or unusable:
// the files of the repo are then unsigned.
func (h *Handler) signer(repo *repos.Repo) *signing.Signer {
	s := h.keys.Signer(signingKey(repo))
	if s == nil {
		h.log.Error("the repo's signing key does not exist or is unusable; its packages and indexes are unsigned",
			"repo", repo.Name, "signing_key", signingKey(repo))
	}
	return s
}

// signState returns the signature of the file whose SHA-256 state is state, or nil (logged)
// when it cannot be made.
func (h *Handler) signState(repo *repos.Repo, s *signing.Signer, state []byte) []byte {
	if s == nil {
		return nil
	}
	sig, err := s.SignSHA256State(state)
	if err != nil {
		h.log.Error("cannot sign a package; it stays unsigned", "repo", repo.Name, "signing_key", signingKey(repo), "err", err)
		return nil
	}
	return sig
}

// ingest adds the package of sp to repo: it parses and checks the package, then commits its pool
// file, with the package identity as an extra lock, and its signature. Under the locks, admit
// refuses a package version that exists with other bytes; onCommitted writes the package's rows
// and the index entries it wins, and marks their architectures for a rebuild, in the commit's
// transaction. The result is unchanged when the same package version is present with the same
// bytes. A client error is an *assets.Refusal. Uploads, the web UI upload and the demo data share
// it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, sp *assets.Spooled, o assets.Origin) (*assets.Result, uploadResult, error) {
	var out uploadResult
	if repo.Kind != repos.KindHosted {
		return nil, out, assets.Refuse(http.StatusBadRequest, "packages are uploaded to a hosted repository")
	}
	info, err := parsePackage(bodyOf(sp), sp.Size)
	if err != nil {
		var bad badPkg
		if errors.As(err, &bad) {
			return nil, out, assets.Refuse(http.StatusBadRequest, "%s", bad.Error())
		}
		return nil, out, fmt.Errorf("parse package: %w", err)
	}
	if info.Arch != "any" && !slices.Contains(architectures(repo), info.Arch) {
		return nil, out, assets.Refuse(http.StatusBadRequest, "architecture %s is not configured for this repository", info.Arch)
	}
	info.withFile(sp.Size, sp.SHA256)
	pool := info.poolPath()
	p, err := repopath.Parse(pool)
	if err != nil {
		return nil, out, assets.Refuse(http.StatusBadRequest, "package name or version too long")
	}
	out = uploadResult{info.Name, info.Version, info.Arch, pool, sp.SHA256}
	ws := []assets.Write{{Repo: repo, Path: p, Body: assets.Body{Spool: sp}, ContentType: pkgContentType,
		Kind: assets.KindArtifact, Origin: o, Locks: []string{"arch:" + info.Name + "/" + info.Version + "/" + info.Arch},
		Meta: &pkgMeta{info: info, sp: sp}}}
	if sig := h.signState(repo, h.signer(repo), info.SigState); sig != nil {
		so := generated
		so.UserID, so.BuildID, so.Created, so.Updated = o.UserID, o.BuildID, o.Created, o.Updated
		ws = append(ws, assets.Write{Repo: repo, Path: p + ".sig", Body: assets.Body{Spool: blobs.Bytes(sig)}, Origin: so, Meta: sigMeta{}})
	}
	rs, err := h.assets.CommitAll(ctx, ws)
	if err != nil {
		return nil, out, err
	}
	return rs[0], out, nil
}

// admit decides a write (format.Spec Admit). A package of an upload keeps a package version that
// exists with the same bytes (unchanged), and refuses one with other bytes (409). The signature
// of an upload keeps a signature that is there. A write without either comes only from the
// server (signatures and indexes) or a proxy fetch: it keeps the same bytes and replaces other
// ones. Every other write, such as a core upload session, is refused (400).
func (h *Handler) admit(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	switch m := w.Meta.(type) {
	case *pkgMeta:
		sum, err := h.q.GetPackageSHA256(ctx, dbq.GetPackageSHA256Params{RepoID: w.Repo.ID, Name: m.info.Name,
			Version: m.info.Version, Arch: m.info.Arch})
		switch {
		case err == nil && sum == w.Body.Digest():
			return assets.Keep, nil
		case err == nil:
			return 0, assets.Refuse(http.StatusConflict, "package version already exists with different content")
		case !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("look up package: %w", err)
		}
		if existing != nil && existing.SHA256 != w.Body.Digest() {
			return 0, assets.Refuse(http.StatusConflict, "%s already exists with different content", w.Path)
		}
		return assets.Store, nil
	case sigMeta:
		if existing != nil {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	if w.Origin.Source != assets.SourceGenerated && w.Origin.Source != assets.SourceProxy {
		return 0, assets.Refuse(http.StatusBadRequest, "Arch packages are uploaded through /arch/{repo}/upload")
	}
	if existing != nil && existing.SHA256 == w.Body.Digest() {
		return assets.Keep, nil
	}
	return assets.Store, nil
}

// newer reports whether a package of order key k and architecture arch takes the index entry
// that a package of key ck and architecture carch holds: a newer version, or the same version for
// the architecture itself over any.
func newer(k []byte, arch string, ck []byte, carch string) bool {
	c := bytes.Compare(k, ck)
	return c > 0 || c == 0 && arch != "any" && carch == "any"
}

// onCommitted writes the package of a stored file, its file list and the index entries it wins,
// and marks their architectures for a rebuild, in the transaction of the write (format.Spec
// OnCommitted).
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	m, ok := w.Meta.(*pkgMeta)
	if !ok {
		return nil
	}
	info, q := m.info, dbq.New(tx)
	vkey := versionKey(info.Version)
	id, err := q.InsertPackage(ctx, dbq.InsertPackageParams{RepoID: w.Repo.ID, Name: info.Name, Version: info.Version,
		Arch: info.Arch, Vkey: vkey, AssetID: a.ID, Filename: a.Path, Size: m.sp.Size, Sha256: m.sp.SHA256,
		SigState: info.SigState, Desc: info.Desc, CreatedAt: time.Now().Unix()})
	if err != nil {
		return err
	}
	if err := q.InsertPackageFiles(ctx, dbq.InsertPackageFilesParams{PackageID: id, Files: info.Files}); err != nil {
		return err
	}
	for _, t := range targets(w.Repo, info.Arch) {
		cur, err := q.GetEntry(ctx, dbq.GetEntryParams{RepoID: w.Repo.ID, Arch: t, Name: info.Name})
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case !newer(vkey, info.Arch, cur.Vkey, cur.Arch):
			continue
		}
		if err := q.UpsertEntry(ctx, dbq.UpsertEntryParams{RepoID: w.Repo.ID, Arch: t, Name: info.Name, PackageID: id}); err != nil {
			return err
		}
		if err := h.rebuilds.MarkTx(ctx, tx, w.Repo.ID, t, unitDB); err != nil {
			return err
		}
	}
	return nil
}

// headBytes is how much of a package a plan reads: makepkg and nfpm write .PKGINFO near the
// start. A package whose .PKGINFO lies past these bytes is checked when the whole file arrives.
const headBytes = 1 << 20

func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Accept:  []string{".zst", ".xz", ".gz"},
		Hint:    "One or more .pkg.tar.zst packages, or a folder of them.",
		Keep:    []string{"**/*.pkg.tar.zst", "**/*.pkg.tar.xz", "**/*.pkg.tar.gz"},
		One:     "package",
		Many:    "packages",
		Note:    "The server reads each package's name, version and architecture, signs it, stores it under pool/, and rebuilds the pacman indexes.",
		Ignored: "Only .pkg.tar.zst, .pkg.tar.xz and .pkg.tar.gz packages are uploaded.",
		Empty:   "The upload has no pacman package.",
		Reads:   []format.ReadRule{{Glob: "**/*.pkg.tar.*", MaxBytes: headBytes}},
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// isPackageFile reports whether a file name is a package of an upload.
func isPackageFile(n string) bool {
	return strings.HasSuffix(n, ".pkg.tar.zst") || strings.HasSuffix(n, ".pkg.tar.xz") || strings.HasSuffix(n, ".pkg.tar.gz")
}

// Plan plans a web UI upload: one item per package file, titled "<name> <version>
// (<architecture>)" from its .PKGINFO, that goes to its pool path. A .PKGINFO that does not parse,
// or an architecture the repo does not configure, is a problem; one past the bytes the plan reads
// is checked at the upload. Other files are left out.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, nil); err != nil {
		return nil, err
	}
	plan := &format.Plan{}
	archs := architectures(repo)
	for _, f := range in.Files {
		if !isPackageFile(f.Name()) {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: f.Path, Files: []format.PlanTarget{{Path: f.Path}}}
		info, found, err := headInfo(f.Head)
		var bad badPkg
		switch {
		case errors.As(err, &bad):
			item.Problems = append(item.Problems, bad.Error())
		case err != nil:
			return nil, fmt.Errorf("parse %s: %w", f.Path, err)
		case !found:
			// .PKGINFO is past the bytes read: the upload parses the whole file.
		default:
			item.Title = info.Name + " " + info.Version + " (" + info.Arch + ")"
			item.Facts = map[string]string{"package": info.Name, "version": info.Version, "architecture": info.Arch}
			if info.Arch != "any" && !slices.Contains(archs, info.Arch) {
				item.Problems = append(item.Problems, "architecture "+info.Arch+" is not configured for this repository")
			}
			item.Files[0].Target = info.poolPath()
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// Store adds each package of a plan (ingest).
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var out []*assets.Result
	for _, it := range plan.Items {
		for _, t := range it.Files {
			sp := files[t.Path]
			if sp == nil {
				return out, format.BadUpload("the upload has no file %s", t.Path)
			}
			res, _, err := h.ingest(ctx, repo, sp, o)
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
