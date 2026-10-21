package nuget

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// push takes a package (PackagePublish/2.0.0): the first part of a multipart/form-data body, or
// a body that is the .nupkg itself. 201 for a new package version, 409 for a version that the
// repo has (also with the same bytes, so dotnet nuget push --skip-duplicate skips it), 400 for a
// package that does not read, 413 above max_upload_size.
func (h *Handler) push(w http.ResponseWriter, r *http.Request, c *route.Call) {
	sp, ok := h.readPush(w, r)
	if !ok {
		return
	}
	defer sp.Close()
	res, info, err := h.ingest(r.Context(), c.Repo, sp, format.OriginOf(c.P, assets.SourcePut))
	switch {
	case err != nil:
		route.Fail(w, r, err)
	case res.Status == assets.Unchanged:
		httpx.Error(w, r, http.StatusConflict, fmt.Sprintf("package %s %s already exists", info.ID, info.Version.Normalized()))
	default:
		w.WriteHeader(http.StatusCreated)
	}
}

// readPush spools the package of a push. On failure it has answered and returns false.
func (h *Handler) readPush(w http.ResponseWriter, r *http.Request) (*assets.Spooled, bool) {
	k := format.Kit{Assets: h.host.Assets}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(mt, "multipart/") {
		return k.SpoolBody(w, r)
	}
	max := h.host.Assets.MaxUploadSize()
	r.Body = http.MaxBytesReader(w, r.Body, max+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "send the package as the first part of multipart/form-data")
		return nil, false
	}
	part, err := mr.NextPart()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "the request has no package part")
		return nil, false
	}
	defer part.Close()
	sp, err := h.host.Assets.Spool(part, max)
	if err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe):
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "upload too large")
		case errors.Is(err, io.ErrUnexpectedEOF):
			httpx.Error(w, r, http.StatusBadRequest, "incomplete request body")
		default:
			route.Fail(w, r, err)
		}
		return nil, false
	}
	return sp, true
}

// ingest stores the package of sp in repo: it reads the root .nuspec, then commits the .nupkg
// (its bytes unchanged) and the .nuspec at their flat-container paths in one commit, with the
// package version as an extra lock. admit keeps a version that exists with the same bytes
// (unchanged) and refuses one with other bytes (409); onCommitted writes the version row in the
// commit's transaction. A client error is an *assets.Refusal. Pushes, uploads, imports and the
// demo data share it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, sp *assets.Spooled, o assets.Origin) (*assets.Result, *pkgInfo, error) {
	info, err := parseNupkg(readerAt(sp), sp.Size)
	if err != nil {
		return nil, nil, asBad(err)
	}
	lid, lver := info.lowerID(), info.lowerVersion()
	np, sp2 := packagePaths(lid, lver)
	nupkg, err := repopath.Parse(np)
	if err != nil {
		return nil, info, assets.Refuse(http.StatusBadRequest, "package id or version too long")
	}
	nuspec, err := repopath.Parse(sp2)
	if err != nil {
		return nil, info, assets.Refuse(http.StatusBadRequest, "package id or version too long")
	}
	lock := "nuget:" + lid + "/" + lver
	rs, err := h.host.Assets.CommitAll(ctx, []assets.Write{
		{Repo: repo, Path: nupkg, Body: assets.Body{Spool: sp}, ContentType: assets.FallbackContentType, Kind: assets.KindArtifact,
			Origin: o, Locks: []string{lock}, Meta: &pushMeta{info: info}},
		{Repo: repo, Path: nuspec, Body: assets.Body{Spool: blobs.Bytes(info.Nuspec)}, ContentType: "application/xml", Kind: assets.KindMetadata,
			Origin: o, Locks: []string{lock}, Meta: &pushMeta{info: info, nuspec: true}},
	})
	if err != nil {
		return nil, info, err
	}
	return rs[0], info, nil
}

// onCommitted writes the version row of a stored .nupkg and moves the revision of its package on,
// in the transaction of the commit (format.Spec OnCommitted).
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	m, ok := w.Meta.(*pushMeta)
	if !ok || m.nuspec {
		return nil
	}
	meta, err := json.Marshal(m.info.Meta)
	if err != nil {
		return err
	}
	v := m.info.Version
	published := w.Origin.Created
	if published.IsZero() {
		published = time.Now()
	}
	q := dbq.New(tx)
	if err := q.InsertVersion(ctx, dbq.InsertVersionParams{RepoID: w.Repo.ID, LowerID: m.info.lowerID(), PackageID: m.info.ID,
		Version: v.Full(), Normalized: v.Normalized(), SortKey: v.SortKey(), Semver2: b2i(v.IsSemVer2()), Prerelease: b2i(v.Prerelease()),
		Meta: string(meta), NupkgPath: a.Path, AssetID: a.ID, PublishedAt: published.Unix()}); err != nil {
		return err
	}
	return bumpPackage(ctx, q, w.Repo.ID, m.info.lowerID())
}

// bumpPackage moves the revision of package lid of a hosted repo on, so its documents render
// anew: the package row gets a revision above every earlier one, also above the revision of a
// package of the same id that was removed before.
func bumpPackage(ctx context.Context, q *dbq.Queries, repoID int64, lid string) error {
	return q.UpsertPackage(ctx, dbq.UpsertPackageParams{RepoID: repoID, LowerID: lid, Rev: time.Now().UnixNano()})
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// onRemoved removes the version of each removed .nupkg, in the transaction of the remove
// (format.Spec OnRemoved), and removes its .nuspec next. A package with versions left gets a new
// revision; a package without goes.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	q := dbq.New(tx)
	var follow []repopath.Path
	changed := map[string]bool{}
	for _, a := range removed {
		lid, lver, file, ok := flatFile(a.Path)
		if !ok || !strings.HasSuffix(file, ".nupkg") {
			continue
		}
		ids, err := q.DeleteVersionsOfAsset(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			changed[id] = true
		}
		_, nuspec := packagePaths(lid, lver)
		if p, err := repopath.Parse(nuspec); err == nil {
			follow = append(follow, p)
		}
	}
	for lid := range changed {
		_, err := q.HasPackage(ctx, dbq.HasPackageParams{RepoID: repo.ID, LowerID: lid})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = q.DeletePackage(ctx, dbq.DeletePackageParams{RepoID: repo.ID, LowerID: lid})
		case err == nil:
			err = bumpPackage(ctx, q, repo.ID, lid)
		}
		if err != nil {
			return nil, err
		}
	}
	return follow, nil
}

// unlist hides a version from the registration and the search (DELETE, NuGet's delete). dotnet
// still restores it by its exact version.
func (h *Handler) unlist(w http.ResponseWriter, r *http.Request, c *route.Call) {
	h.setListed(w, r, c, false)
}

// relist lists an unlisted version again (POST).
func (h *Handler) relist(w http.ResponseWriter, r *http.Request, c *route.Call) {
	h.setListed(w, r, c, true)
}

func (h *Handler) setListed(w http.ResponseWriter, r *http.Request, c *route.Call, listed bool) {
	id := r.PathValue("id")
	v, err := version.ParseNuGet(r.PathValue("ver"))
	if !validID(id) || err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "give a package id and a NuGet version")
		return
	}
	action := ActionUnlist
	if listed {
		action = ActionRelist
	}
	found := false
	err = h.host.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		q := dbq.New(tx)
		n, err := q.SetListed(r.Context(), dbq.SetListedParams{Listed: b2i(listed), RepoID: c.Repo.ID,
			LowerID: strings.ToLower(id), SortKey: v.SortKey()})
		if err != nil || n == 0 {
			return err
		}
		found = true
		if err := bumpPackage(r.Context(), q, c.Repo.ID, strings.ToLower(id)); err != nil {
			return err
		}
		return h.host.Audit.RecordTx(r.Context(), tx, audit.Event{Action: action, RepoID: c.Repo.ID, Repo: c.Repo.Name,
			Target: id + " " + v.Normalized()})
	})
	switch {
	case err != nil:
		route.Fail(w, r, err)
	case !found:
		httpx.Error(w, r, http.StatusNotFound, fmt.Sprintf("package %s %s does not exist", id, v.Normalized()))
	default:
		w.WriteHeader(http.StatusOK)
	}
}
