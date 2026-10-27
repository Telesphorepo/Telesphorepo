package rpm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// generated is the origin of the files the server makes: the repodata and re-signed packages.
var generated = assets.Origin{Source: assets.SourceGenerated}

type uploadResult struct {
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"` // of the stored, signed file
}

// pkgMeta is the Meta of the write of an uploaded package.
type pkgMeta struct {
	info        *pkgInfo
	uploadSHA   string // of the file as uploaded
	headerStart int64  // in the stored file
	stored      *assets.Spooled
}

// resignMeta is the Meta of the write of a package signed again with a new key.
type resignMeta struct {
	id          int64
	headerStart int64
	from        string // the SHA-256 of the file it was made from, the row's
}

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
	st, out, err := h.ingest(r.Context(), c.Repo, sp, format.OriginOf(c.P, assets.SourcePut))
	switch {
	case err != nil:
		route.Fail(w, r, err)
	case st != assets.Unchanged:
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

// signer returns the signer of repo's key, or nil (logged) when the key is missing or unusable:
// the files of the repo are then unsigned.
func (h *Handler) signer(repo *repos.Repo) *signing.Signer {
	s := h.keys.Signer(signingKey(repo))
	if s == nil {
		h.log.Error("the repo's signing key does not exist or is unusable; its packages and repodata are unsigned",
			"repo", repo.Name, "signing_key", signingKey(repo))
	}
	return s
}

// signState returns the signature of the header whose SHA-256 state is state, or nil (logged)
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

// parse reads the package of sp; a client error is a 400 *assets.Refusal.
func parse(sp *assets.Spooled) (*pkgInfo, error) {
	r, err := sp.Reader()
	if err != nil {
		return nil, err
	}
	info, err := readPackage(bufio.NewReaderSize(r, 64<<10))
	var bad badPkg
	if errors.As(err, &bad) {
		return nil, assets.Refuse(http.StatusBadRequest, "%s", bad.Error())
	}
	return info, err
}

// ingest adds the package of sp to repo: it reads and checks the package, signs it with the
// repo's key into a new file (the payload checked against its digest on the way), and commits it
// with the package identity as an extra lock. Under the locks, admit keeps a package that exists
// with the same uploaded bytes and refuses other bytes; onCommitted writes the package's rows and
// marks the repodata. An identical upload is kept without signing it again. A client error is an
// *assets.Refusal. Uploads, the web UI upload, the Nexus import and the demo data share it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, sp *assets.Spooled, o assets.Origin) (assets.Status, uploadResult, error) {
	var out uploadResult
	if repo.Kind != repos.KindHosted {
		return "", out, assets.Refuse(http.StatusBadRequest, "packages are uploaded to a hosted repository")
	}
	info, err := parse(sp)
	if err != nil {
		return "", out, err
	}
	p, err := repopath.Parse(info.path())
	if err != nil {
		return "", out, assets.Refuse(http.StatusBadRequest, "package name or version too long")
	}
	out = uploadResult{Package: info.Name, Version: info.evr(), Architecture: info.Arch, Filename: string(p)}
	sums, err := h.q.GetSums(ctx, identity(repo.ID, info))
	switch {
	case err == nil && sums.UploadSha256 == sp.SHA256:
		out.SHA256 = sums.Sha256
		return assets.Unchanged, out, nil
	case err == nil:
		return "", out, assets.Refuse(http.StatusConflict, "package version already exists with different content")
	case !errors.Is(err, sql.ErrNoRows):
		return "", out, err
	}
	src, err := sp.ReaderAt()
	if err != nil {
		return "", out, err
	}
	stored, start, err := h.signedFile(repo, src, sp.Size, info, h.signer(repo))
	if err != nil {
		var bad badPkg
		if errors.As(err, &bad) {
			return "", out, assets.Refuse(http.StatusBadRequest, "%s", bad.Error())
		}
		return "", out, err
	}
	defer stored.Close()
	res, err := h.assets.Commit(ctx, assets.Write{Repo: repo, Path: p, Body: assets.Body{Spool: stored},
		ContentType: rpmContentType, Kind: assets.KindArtifact, Origin: o,
		Locks: []string{"rpm:" + info.Name + "/" + info.evr() + "/" + info.Arch},
		Meta:  &pkgMeta{info: info, uploadSHA: sp.SHA256, headerStart: start, stored: stored}})
	if err != nil {
		return "", out, err
	}
	out.SHA256 = res.Asset.SHA256
	return res.Status, out, nil
}

// identity is the UNIQUE key of a package of repo.
func identity(repoID int64, info *pkgInfo) dbq.GetSumsParams {
	return dbq.GetSumsParams{RepoID: repoID, Name: info.Name, Epoch: info.Epoch, Version: info.Version,
		Release: info.Release, Arch: info.Arch}
}

// signedFile writes the stored file of a package to a temp file: src with its signature header
// replaced by one that holds the signature by s (none when s is nil). It returns the file and
// where the header starts in it.
func (h *Handler) signedFile(repo *repos.Repo, src io.ReaderAt, size int64, info *pkgInfo, s *signing.Signer) (*assets.Spooled, int64, error) {
	sigh, err := signedSigHeader(info.sig, info.Format, h.signState(repo, s, info.hdrState))
	if err != nil {
		return nil, 0, err
	}
	out, err := h.newTemp()
	if err != nil {
		return nil, 0, err
	}
	start, err := assemble(out, src, size, info, sigh)
	if err != nil {
		out.remove()
		return nil, 0, err
	}
	sp, err := out.spooled()
	if err != nil {
		out.remove()
		return nil, 0, err
	}
	return sp, start, nil
}

// admit decides a write (format.Spec Admit). An uploaded package is kept when the package exists
// with the same uploaded bytes (unchanged) and refused with other bytes, or when another package
// holds its file name (a package that differs only by its epoch); 409. A package signed again
// replaces its file. Every other write comes from the server (the repodata) or a proxy fetch: it
// keeps the same bytes and replaces other ones; any other, such as a core upload session, is
// refused (400).
func (h *Handler) admit(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	switch m := w.Meta.(type) {
	case *pkgMeta:
		sums, err := h.q.GetSums(ctx, identity(w.Repo.ID, m.info))
		switch {
		case err == nil && sums.UploadSha256 == m.uploadSHA:
			return assets.Keep, nil
		case err == nil:
			return 0, assets.Refuse(http.StatusConflict, "package version already exists with different content")
		case !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("look up package: %w", err)
		}
		if existing != nil {
			return 0, assets.Refuse(http.StatusConflict, "%s already holds another package (the same name, version, release and architecture with another epoch)", w.Path)
		}
		return assets.Store, nil
	case *resignMeta:
		// A package deleted or written again since its page was read stays as it is.
		if existing == nil || existing.SHA256 != m.from {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	if w.Origin.Source != assets.SourceGenerated && w.Origin.Source != assets.SourceProxy {
		return 0, assets.Refuse(http.StatusBadRequest, "RPM packages are uploaded through /rpm/{repo}/upload")
	}
	if existing != nil && existing.SHA256 == w.Body.Digest() {
		return assets.Keep, nil
	}
	return assets.Store, nil
}

// onCommitted writes the rows of a stored package and marks the repodata, or records the new file
// of a package signed again, in the transaction of the write (format.Spec OnCommitted).
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	q := dbq.New(tx)
	switch m := w.Meta.(type) {
	case *resignMeta:
		if err := q.UpdateStored(ctx, dbq.UpdateStoredParams{AssetID: a.ID, Size: a.Size, Sha256: a.SHA256, HeaderStart: m.headerStart, ID: m.id}); err != nil {
			return err
		}
		// Each batch of a key change brings the repodata after it, so dnf finds the new
		// checksums one debounce after the batch, not after the whole run.
		return h.rebuilds.MarkTx(ctx, tx, w.Repo.ID, groupRepodata, unitIndex)
	case *pkgMeta:
		info := m.info
		id, err := q.InsertPackage(ctx, dbq.InsertPackageParams{RepoID: w.Repo.ID, Name: info.Name, Epoch: info.Epoch,
			Version: info.Version, Release: info.Release, Arch: info.Arch, Evr: info.evr(),
			Vkey: versionKey(info.Epoch, info.Version, info.Release), AssetID: a.ID, Filename: a.Path,
			Size: m.stored.Size, Sha256: m.stored.SHA256, UploadSha256: m.uploadSHA, Format: int64(info.Format),
			HdrState: info.hdrState, HeaderStart: m.headerStart, HeaderLen: info.headerLen, BuildTime: info.BuildTime,
			InstalledSize: info.InstalledSize, ArchiveSize: info.ArchiveSize, Summary: info.Summary, CreatedAt: time.Now().Unix()})
		if err != nil {
			return err
		}
		pt := info.parts
		if err := q.InsertParts(ctx, dbq.InsertPartsParams{PackageID: id, Text: pt.Text, FormatA: pt.FormatA,
			FormatB: pt.FormatB, Files: pt.Files, Other: pt.Other}); err != nil {
			return err
		}
		return h.rebuilds.MarkTx(ctx, tx, w.Repo.ID, groupRepodata, unitIndex)
	}
	return nil
}

// headBytes is how much of a package a plan reads: the lead and both headers of most packages.
// A package whose header lies past these bytes is checked when the whole file arrives.
const headBytes = 4 << 20

func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Accept:  []string{".rpm"},
		Hint:    "One or more .rpm packages, or a folder of them.",
		Keep:    []string{"**/*.rpm"},
		One:     "package",
		Many:    "packages",
		Note:    "The server reads each package's name, version and architecture, signs it with the repository's key, stores it under Packages/, and rebuilds the repodata.",
		Ignored: "Only .rpm packages are uploaded.",
		Empty:   "The upload has no RPM package.",
		Reads:   []format.ReadRule{{Glob: "**/*.rpm", MaxBytes: headBytes}},
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// Plan plans a web UI upload: one item per .rpm file, titled "<name> <version> (<architecture>)"
// from its header, that goes to its path under Packages/. A header that does not parse is a
// problem; one past the bytes the plan reads is checked at the upload. Other files are left out.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, nil); err != nil {
		return nil, err
	}
	plan := &format.Plan{}
	for _, f := range in.Files {
		if !strings.HasSuffix(f.Name(), ".rpm") {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: f.Path, Files: []format.PlanTarget{{Path: f.Path}}}
		info, err := readPackage(bytes.NewReader(f.Head))
		var bad badPkg
		switch {
		case errors.As(err, &bad) && f.Truncated() && strings.Contains(bad.Error(), "ends"):
			// The header lies past the bytes read: the upload reads the whole file.
		case errors.As(err, &bad):
			item.Problems = append(item.Problems, bad.Error())
		case err != nil:
			return nil, fmt.Errorf("parse %s: %w", f.Path, err)
		default:
			item.Title = info.Name + " " + info.evr() + " (" + info.Arch + ")"
			item.Facts = map[string]string{"package": info.Name, "version": info.evr(), "architecture": info.Arch}
			item.Files[0].Target = info.path()
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
			st, res, err := h.ingest(ctx, repo, sp, o)
			var ref *assets.Refusal
			if errors.As(err, &ref) {
				return out, assets.Refuse(ref.Status, "%s: %s", t.Path, ref.Message)
			}
			if err != nil {
				return out, err
			}
			a, err := h.assets.Get(ctx, repo.ID, res.Filename)
			if err != nil {
				return out, err
			}
			out = append(out, &assets.Result{Asset: a, Status: st})
		}
	}
	return out, nil
}

// tempOut is a buffered temp file that hashes everything written to it.
type tempOut struct {
	f                 *os.File
	bw                *bufio.Writer
	md5, sha1, sha256 hash.Hash
	n                 int64
}

func (h *Handler) newTemp() (*tempOut, error) {
	dir := h.settings.Current().TmpDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "rpm-*")
	if err != nil {
		return nil, err
	}
	return &tempOut{f: f, bw: bufio.NewWriterSize(f, 64<<10), md5: md5.New(), sha1: sha1.New(), sha256: sha256.New()}, nil
}

func (t *tempOut) Write(p []byte) (int, error) {
	n, err := t.bw.Write(p)
	t.md5.Write(p[:n])
	t.sha1.Write(p[:n])
	t.sha256.Write(p[:n])
	t.n += int64(n)
	return n, err
}

// spooled returns the file as the body of a write; closing the body removes the file.
func (t *tempOut) spooled() (*assets.Spooled, error) {
	if err := t.bw.Flush(); err != nil {
		return nil, err
	}
	if _, err := t.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return &assets.Spooled{File: t.f, Size: t.n,
		SHA256: hex.EncodeToString(t.sha256.Sum(nil)),
		SHA1:   hex.EncodeToString(t.sha1.Sum(nil)),
		MD5:    hex.EncodeToString(t.md5.Sum(nil))}, nil
}

func (t *tempOut) remove() {
	t.f.Close()
	os.Remove(t.f.Name())
}
