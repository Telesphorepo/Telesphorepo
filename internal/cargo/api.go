package cargo

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/cargo/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// maxIndexRows is the most versions of one crate that an index file lists.
const maxIndexRows = 50000

// maxIndexBytes is the largest index file of a remote that a proxy serves.
const maxIndexBytes = 64 << 20

// routes mounts the registry (Cargo book, Registry Index and Web API). Cargo sends its token as a
// raw Authorization header, so every route reads one (TokenHeader); refusals are Cargo errors.
func (h *Handler) routes(r *route.Router, k format.Kit) {
	read := route.FormatRead(name)
	read.TokenHeader, read.Errors = "Authorization", writeRefusal
	cfg := read
	cfg.Challenge = h.challenge
	write := route.FormatWrite(name)
	write.TokenHeader, write.Errors = "Authorization", writeRefusal
	none := route.Policy{Anonymous: true, Errors: writeRefusal}

	r.Handle("GET /cargo/{repo}/index/config.json", cfg, h.config)
	r.Handle("GET /cargo/{repo}/index/{path...}", read, h.index)
	r.Handle("GET /cargo/{repo}/crates/{name}/{file}", read, h.download)
	r.Handle("PUT /cargo/{repo}/api/v1/crates/new", write, h.publish)
	r.Handle("DELETE /cargo/{repo}/api/v1/crates/{name}/{version}/yank", write, h.yank(true))
	r.Handle("PUT /cargo/{repo}/api/v1/crates/{name}/{version}/unyank", write, h.yank(false))
	r.Handle("GET /cargo/{repo}/api/v1/crates", none, unsupported("cargo search is not served: browse the crates of this repository in the web UI"))
	r.Handle("/cargo/{repo}/api/v1/crates/{name}/owners", none, unsupported("crate owners are not kept: who may publish to this repository is set in its access settings in the web UI"))
}

// cargoError is the error body of the Cargo Web API: {"errors": [{"detail": "..."}]}.
func cargoError(w http.ResponseWriter, status int, detail string) {
	httpx.WriteJSON(w, status, map[string]any{"errors": []map[string]string{{"detail": detail}}})
}

// writeRefusal writes a refusal of the route policy as a Cargo error.
func writeRefusal(w http.ResponseWriter, _ *http.Request, d access.Decision) {
	cargoError(w, d.Status, d.Message)
}

// fail answers err as a Cargo error: the status of a refusal, 502 for a remote that did not
// answer, else 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var se httpx.StatusError
	switch {
	case errors.As(err, &se):
		cargoError(w, se.HTTPStatus(), se.Error())
	case errors.Is(err, assets.ErrRemoteFailed):
		cargoError(w, http.StatusBadGateway, "The remote repository did not answer.")
	case errors.Is(err, context.Canceled):
		cargoError(w, http.StatusServiceUnavailable, "request canceled")
	default:
		h.host.Log.ErrorContext(r.Context(), "cargo request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		cargoError(w, http.StatusInternalServerError, "internal error")
	}
}

func unsupported(detail string) route.Handler {
	return func(w http.ResponseWriter, _ *http.Request, _ *route.Call) {
		cargoError(w, http.StatusNotFound, detail)
	}
}

// challenge is the WWW-Authenticate of a 401 to config.json: Cargo then sends its token, and
// cargo login names the repo page.
func (h *Handler) challenge(w http.ResponseWriter, r *http.Request, _ access.Decision) {
	login := h.host.API.BaseURL(r) + "/#/repo/" + url.PathEscape(r.PathValue("repo"))
	w.Header().Set("WWW-Authenticate", `Cargo login_url="`+login+`"`)
}

// encode is the JSON of v without HTML escapes, so the URL templates keep their braces as they are.
func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.Bytes()
}

// config serves config.json: the crates and the API of this repo, and auth-required when a person
// must sign in to read it, so Cargo sends its token with every request.
func (h *Handler) config(w http.ResponseWriter, r *http.Request, c *route.Call) {
	base := h.host.API.BaseURL(r) + "/cargo/" + c.Repo.Name
	doc := struct {
		DL           string `json:"dl"`
		API          string `json:"api"`
		AuthRequired bool   `json:"auth-required,omitempty"`
	}{DL: base + "/crates/{crate}/{crate}-{version}.crate", API: base, AuthRequired: !c.Repo.AnonymousRead}
	format.WriteDocument(w, r, "application/json", format.NewDocument(encode(doc)))
}

// index serves the index file of a crate.
func (h *Handler) index(w http.ResponseWriter, r *http.Request, c *route.Call) {
	crate, ok := nameOfIndexPath(r.PathValue("path"))
	if !ok {
		cargoError(w, http.StatusNotFound, "not an index file")
		return
	}
	d, err := h.indexOf(r.Context(), r, c.P, c.Repo, crate)
	if err == nil && d == nil {
		err = errNoCrate
	}
	if err == nil {
		err = h.docs.Serve(w, r, "application/json", d.key, d.build)
	}
	switch {
	case errors.Is(err, errNoCrate):
		cargoError(w, http.StatusNotFound, "crate "+crate+" is not in this repository")
	case err != nil:
		h.fail(w, r, err)
	}
}

// errNoCrate is a crate that a repo does not have.
var errNoCrate = errors.New("no such crate")

// indexDoc is the index file of a crate: its key in the document cache and the build of its body.
type indexDoc struct {
	key   string
	build func() ([]byte, error)
}

// indexOf returns the index file of crate (lowercase) in repo for p: rendered from the rows of a
// hosted repo (keyed by the revision of the crate), the remote's file of a proxy (cached under
// .index/, keyed by its SHA-256), and in a group the file of the first member that has the crate
// (assets.Reader.Owner). nil when there is none. Only the build reads the rows or the file.
func (h *Handler) indexOf(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, crate string) (*indexDoc, error) {
	switch repo.Kind {
	case repos.KindHosted:
		n, err := h.q.GetNameRev(ctx, dbq.GetNameRevParams{RepoID: repo.ID, FoldedName: fold(crate)})
		if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.ToLower(n.Name) != crate) {
			return nil, nil // the folded name is held by a crate of another lowercase name
		}
		if err != nil {
			return nil, err
		}
		key := format.DocumentKey("hosted", strconv.FormatInt(repo.ID, 10), crate, strconv.FormatInt(n.Rev, 10))
		return &indexDoc{key: key, build: func() ([]byte, error) { return h.renderIndex(ctx, repo.ID, crate) }}, nil
	case repos.KindProxy:
		a, err := h.host.Reader.Resolve(ctx, r, p, repo, repopath.Path(".index/"+indexPath(crate)))
		if err != nil || a == nil {
			return nil, err
		}
		return &indexDoc{key: format.DocumentKey("proxy", a.SHA256), build: func() ([]byte, error) {
			return h.host.Assets.ReadAll(ctx, a, maxIndexBytes)
		}}, nil
	case repos.KindGroup:
		var d *indexDoc
		owner, err := h.host.Reader.Owner(ctx, r, p, repo, func(ctx context.Context, m *repos.Repo) (bool, error) {
			x, err := h.indexOf(ctx, r, p, m, crate)
			if x != nil {
				d = x
			}
			return x != nil, err
		})
		if err != nil || owner == nil {
			return nil, err
		}
		return d, nil
	}
	return nil, nil
}

// renderIndex renders the index file of crate (lowercase) of a hosted repo from its rows, one
// line per version with its yanked flag.
func (h *Handler) renderIndex(ctx context.Context, repoID int64, crate string) ([]byte, error) {
	rows, err := h.q.ListEntries(ctx, dbq.ListEntriesParams{RepoID: repoID, LowerName: crate, Limit: maxIndexRows})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errNoCrate
	}
	var b bytes.Buffer
	for _, row := range rows {
		l, err := withYanked(row.Entry, row.Yanked != 0)
		if err != nil {
			return nil, err
		}
		b.Write(l)
	}
	return b.Bytes(), nil
}

// download serves a .crate. A group serves it from the member that owns the crate's name, so a
// later member cannot shadow it: a hosted member owns it when it has the name, a proxy when it
// has the remote's index file of the crate.
func (h *Handler) download(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := "crates/" + r.PathValue("name") + "/" + r.PathValue("file")
	crate, _, ok := parseCratePath(p)
	if !ok {
		cargoError(w, http.StatusNotFound, "not a crate file")
		return
	}
	if c.Repo.Kind != repos.KindGroup {
		h.host.Reader.Serve(w, r, c, repopath.Path(p))
		return
	}
	ctx := r.Context()
	lower := strings.ToLower(crate)
	owner, err := h.host.Reader.Owner(ctx, r, c.P, c.Repo, func(ctx context.Context, m *repos.Repo) (bool, error) {
		d, err := h.indexOf(ctx, r, c.P, m, lower)
		return d != nil, err
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var a *assets.Asset
	if owner != nil {
		if a, err = h.host.Reader.Resolve(ctx, r, c.P, owner, repopath.Path(p)); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	if a == nil {
		cargoError(w, http.StatusNotFound, "not found")
		return
	}
	h.host.Reader.ServeAsset(w, r, a, assets.ServeOptions{})
}

// publish takes a cargo publish (Cargo book, Web API: Publish): a 32-bit little-endian length,
// the JSON of the crate, a length and the .crate. The index entry comes from the JSON; the name
// and the version must be those of the Cargo.toml in the crate.
func (h *Handler) publish(w http.ResponseWriter, r *http.Request, c *route.Call) {
	ctx := r.Context()
	maxJSON := settings.MaxJSONSize.Get(h.host.API.Settings())
	maxCrate := h.host.Assets.MaxUploadSize()
	body := http.MaxBytesReader(w, r.Body, maxJSON+maxCrate+8)
	var n uint32
	if err := binary.Read(body, binary.LittleEndian, &n); err != nil {
		cargoError(w, http.StatusBadRequest, "the request is not a cargo publish")
		return
	}
	if int64(n) > maxJSON {
		cargoError(w, http.StatusRequestEntityTooLarge, "the metadata of the crate is too large")
		return
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(body, raw); err != nil {
		cargoError(w, http.StatusBadRequest, "the request ended early")
		return
	}
	var meta publishMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		cargoError(w, http.StatusBadRequest, "the metadata of the crate is not valid JSON")
		return
	}
	if err := binary.Read(body, binary.LittleEndian, &n); err != nil {
		cargoError(w, http.StatusBadRequest, "the request ended early")
		return
	}
	if int64(n) > maxCrate {
		cargoError(w, http.StatusRequestEntityTooLarge, "the crate is larger than the upload limit of this server")
		return
	}
	sp, err := h.host.Assets.Spool(io.LimitReader(body, int64(n)), maxCrate)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer sp.Close()
	if sp.Size != int64(n) {
		cargoError(w, http.StatusBadRequest, "the request ended early")
		return
	}
	e := entryOfPublish(&meta, sp.SHA256)
	if msg := sameCrate(sp, e); msg != "" {
		cargoError(w, http.StatusBadRequest, msg)
		return
	}
	res, err := h.ingest(ctx, c.Repo, e, sp, format.OriginOf(c.P, assets.SourcePut))
	switch {
	case err != nil:
		h.fail(w, r, err)
	case res.Status == assets.Unchanged:
		cargoError(w, http.StatusConflict, "crate "+e.Name+" version "+e.Vers+" is already uploaded")
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"warnings": map[string][]string{
			"invalid_categories": {}, "invalid_badges": {}, "other": {}}})
	}
}

// sameCrate checks that the .crate of sp holds the crate that e names: "" when it does, else why
// not.
func sameCrate(sp *assets.Spooled, e *entry) string {
	b, err := readManifest(io.NewSectionReader(readerAt(sp), 0, sp.Size))
	var bc badCrate
	switch {
	case errors.As(err, &bc):
		return bc.Error()
	case err != nil:
		return "the .crate ended early"
	}
	m, err := entryOfManifest(b, sp.SHA256, func(string) bool { return true })
	if errors.As(err, &bc) {
		return bc.Error()
	} else if err != nil {
		return err.Error()
	}
	if m.Name != e.Name || m.Vers != e.Vers {
		return "the metadata names " + e.Name + " " + e.Vers + ", but the Cargo.toml of the .crate names " + m.Name + " " + m.Vers
	}
	return ""
}

// readerAt returns the content of sp.
func readerAt(sp *assets.Spooled) io.ReaderAt {
	if sp.File != nil {
		return sp.File
	}
	return bytes.NewReader(sp.Data)
}

// yank sets the yanked flag of a version (cargo yank, cargo yank --undo): {"ok": true}.
func (h *Handler) yank(yanked bool) route.Handler {
	action := ActionYank
	if !yanked {
		action = ActionUnyank
	}
	return func(w http.ResponseWriter, r *http.Request, c *route.Call) {
		crate, vers := r.PathValue("name"), r.PathValue("version")
		v, err := version.ParseSemVer(vers)
		if err != nil || !crateNameRE.MatchString(crate) {
			cargoError(w, http.StatusNotFound, "crate "+crate+" has no version "+vers+" in this repository")
			return
		}
		flag := int64(0)
		if yanked {
			flag = 1
		}
		var changed int64
		err = h.host.DB.Tx(r.Context(), func(tx *sql.Tx) error {
			var err error
			q := dbq.New(tx)
			changed, err = q.SetYanked(r.Context(), dbq.SetYankedParams{Yanked: flag, RepoID: c.Repo.ID,
				LowerName: strings.ToLower(crate), SortKey: v.SortKey()})
			if err != nil || changed == 0 {
				return err
			}
			return q.BumpRev(r.Context(), dbq.BumpRevParams{At: time.Now().UnixNano(), RepoID: c.Repo.ID, FoldedName: fold(crate)})
		})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if changed == 0 {
			cargoError(w, http.StatusNotFound, "crate "+crate+" has no version "+vers+" in this repository")
			return
		}
		_ = h.host.Audit.Record(r.Context(), audit.Event{Action: action, RepoID: c.Repo.ID, Repo: c.Repo.Name, Target: crate + "@" + vers})
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
