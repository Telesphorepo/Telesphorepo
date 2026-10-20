package npm

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

const (
	maxMetaValue = 64 << 20 // the largest value of a publish body other than the tarball
	maxSigstore  = 16 << 20 // the largest provenance bundle
)

// putBody is a PUT <name> body: its fields without _attachments, the tarball of the attachment
// (spooled, decoded from base64) and the provenance bundle.
type putBody struct {
	fields   map[string]json.RawMessage
	tarball  *assets.Spooled
	sigstore []byte
}

func (b *putBody) close() {
	if b.tarball != nil {
		b.tarball.Close()
	}
}

// readPut reads a PUT body as a stream: each field whole, except the data of a .tgz attachment,
// which goes base64-decoded into the spool under max_upload_size. A client mistake is an
// *assets.Refusal.
func (h *Handler) readPut(w http.ResponseWriter, r *http.Request) (*putBody, error) {
	max := h.assets.MaxUploadSize()
	body := http.MaxBytesReader(w, r.Body, max/3*4+maxMetaValue)
	s := newScanner(body)
	out := &putBody{fields: map[string]json.RawMessage{}}
	bad := func(err error) (*putBody, error) {
		out.close()
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || errors.Is(err, assets.ErrTooLarge) {
			return nil, assets.Refuse(http.StatusRequestEntityTooLarge, "upload too large")
		}
		var ref *assets.Refusal
		if errors.As(err, &ref) {
			return nil, err
		}
		return nil, assets.Refuse(http.StatusBadRequest, "%v", err)
	}
	if err := s.expect('{'); err != nil {
		return bad(err)
	}
	for first := true; ; first = false {
		more, err := s.more(first, '}')
		if err != nil {
			return bad(err)
		}
		if !more {
			return out, nil
		}
		k, err := s.key(4096)
		if err != nil {
			return bad(err)
		}
		if k != "_attachments" {
			if out.fields[k], err = s.raw(maxMetaValue); err != nil {
				return bad(err)
			}
			continue
		}
		if err := h.readAttachments(s, out, max); err != nil {
			return bad(err)
		}
	}
}

// readAttachments reads the _attachments object: one .tgz (base64) and at most one .sigstore (a
// JSON string). Other attachments are refused.
func (h *Handler) readAttachments(s *scanner, out *putBody, max int64) error {
	if err := s.expect('{'); err != nil {
		return err
	}
	for first := true; ; first = false {
		more, err := s.more(first, '}')
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		file, err := s.key(4096)
		if err != nil {
			return err
		}
		tgz, sig := strings.HasSuffix(file, ".tgz"), strings.HasSuffix(file, ".sigstore")
		switch {
		case !tgz && !sig:
			return assets.Refuse(http.StatusBadRequest, "unknown attachment %q: a publish carries the package tarball", file)
		case tgz && out.tarball != nil, sig && out.sigstore != nil:
			return assets.Refuse(http.StatusBadRequest, "a publish carries one tarball")
		}
		if err := s.expect('{'); err != nil {
			return err
		}
		for first := true; ; first = false {
			more, err := s.more(first, '}')
			if err != nil {
				return err
			}
			if !more {
				break
			}
			k, err := s.key(4096)
			if err != nil {
				return err
			}
			switch {
			case k == "data" && tgz:
				if out.tarball, err = h.spoolBase64(s, max); err != nil {
					return err
				}
			case k == "data":
				raw, err := s.raw(maxSigstore)
				if err != nil {
					return err
				}
				var data string
				if json.Unmarshal(raw, &data) != nil {
					return assets.Refuse(http.StatusBadRequest, "the provenance bundle must be a JSON string")
				}
				out.sigstore = []byte(data)
			default:
				if _, err := s.raw(4096); err != nil {
					return err
				}
			}
		}
		if tgz && out.tarball == nil {
			return assets.Refuse(http.StatusBadRequest, "the tarball attachment %q has no data", file)
		}
	}
}

// spoolBase64 decodes the string value at s (base64) into the spool, at most max bytes.
func (h *Handler) spoolBase64(s *scanner, max int64) (*assets.Spooled, error) {
	pr, pw := io.Pipe()
	type result struct {
		sp  *assets.Spooled
		err error
	}
	done := make(chan result, 1)
	go func() {
		sp, err := h.assets.Spool(base64.NewDecoder(base64.StdEncoding, pr), max)
		pr.CloseWithError(err) // a decode error ends the writer too
		done <- result{sp, err}
	}()
	err := s.streamString(pw)
	pw.CloseWithError(err)
	res := <-done
	switch {
	case err != nil:
		if res.sp != nil {
			res.sp.Close()
		}
		return nil, err
	case res.err != nil:
		var cie base64.CorruptInputError
		if errors.As(res.err, &cie) {
			return nil, assets.Refuse(http.StatusBadRequest, "the tarball attachment is not valid base64")
		}
		return nil, res.err
	}
	return res.sp, nil
}

// putPackage serves PUT <name> and PUT <name>/-rev/<rev>. A body with a tarball publishes a
// version. A body without one updates the metadata: a changed deprecated message of a version is
// applied, and on the -rev route (npm unpublish, which needs repo.delete) every version missing
// from the body is removed with its tarball.
func (h *Handler) putPackage(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, rev string) {
	b, err := h.readPut(w, r)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	defer b.close()
	var bodyName string
	json.Unmarshal(b.fields["name"], &bodyName)
	if bodyName != "" && bodyName != pkg {
		httpx.Error(w, r, http.StatusBadRequest, "the body names the package "+bodyName+", the URL "+pkg)
		return
	}
	if b.tarball != nil {
		h.publish(w, r, c, pkg, b)
		return
	}
	h.update(w, r, c, pkg, b, rev)
}

// publish adds the one version of a publish body with its tarball.
func (h *Handler) publish(w http.ResponseWriter, r *http.Request, c *route.Call, pkg string, b *putBody) {
	if err := checkNewName(pkg); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, pkg+": "+err.Error())
		return
	}
	var versions map[string]map[string]json.RawMessage
	if json.Unmarshal(b.fields["versions"], &versions) != nil || len(versions) != 1 {
		httpx.Error(w, r, http.StatusBadRequest, "a publish names exactly one version")
		return
	}
	var v string
	var manifest map[string]json.RawMessage
	for v, manifest = range versions {
	}
	if _, err := version.ParseSemVer(v); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return
	}
	var mv struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	raw, _ := json.Marshal(manifest)
	json.Unmarshal(raw, &mv)
	if mv.Name != pkg || mv.Version != v {
		httpx.Error(w, r, http.StatusBadRequest, "the version manifest must name "+pkg+" and "+v)
		return
	}
	normalize(manifest, pkg, v)
	var tags map[string]string
	json.Unmarshal(b.fields["dist-tags"], &tags)
	m := &publishMeta{name: pkg, version: v, manifest: manifest}
	for t, tv := range tags {
		if tv == v && validTag(t) {
			m.tags = append(m.tags, t)
		}
	}
	slices.Sort(m.tags)
	if _, err := h.addVersion(r.Context(), c.Repo, m, b.tarball, b.sigstore, format.OriginOf(c.P, assets.SourcePut)); err != nil {
		route.Fail(w, r, err)
		return
	}
	ok(w, http.StatusCreated)
}

// update applies the metadata of a body without a tarball (see putPackage).
func (h *Handler) update(w http.ResponseWriter, r *http.Request, c *route.Call, pkg string, b *putBody, rev string) {
	var versions map[string]struct {
		Deprecated json.RawMessage `json:"deprecated"`
	}
	if raw, ok := b.fields["versions"]; ok && json.Unmarshal(raw, &versions) != nil {
		httpx.Error(w, r, http.StatusBadRequest, "versions must be an object of version manifests")
		return
	}
	ctx := r.Context()
	var gone []repopath.Path
	err := h.change(ctx, c.Repo, pkg, func(q *dbq.Queries) (bool, error) {
		d, err := packageOf(ctx, q, c.Repo.ID, pkg)
		if err != nil {
			if errors.Is(err, errNoPackage) {
				return false, assets.Refuse(http.StatusNotFound, "%s has no published version", pkg)
			}
			return false, err
		}
		if rev != "" && rev != d.pkg.Rev {
			return false, assets.Refuse(http.StatusConflict, "%s changed since it was read; run the command again", pkg)
		}
		if err := d.load(ctx, q); err != nil {
			return false, err
		}
		changed := false
		for _, v := range d.versions {
			bv, inBody := versions[v.Version]
			if !inBody {
				if rev != "" {
					gone = append(gone, repopath.Path(v.Tarball))
				}
				continue
			}
			dep := ""
			json.Unmarshal(bv.Deprecated, &dep)
			if dep != v.Deprecated {
				if err := q.SetDeprecated(ctx, dbq.SetDeprecatedParams{Deprecated: dep, RepoID: c.Repo.ID, Name: pkg, Version: v.Version}); err != nil {
					return false, err
				}
				changed = true
			}
		}
		return changed, nil
	})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if len(gone) > 0 {
		if _, err := h.assets.Remove(ctx, assets.Removal{Repo: c.Repo, Paths: gone, Origin: format.OriginOf(c.P, assets.SourcePut)}); err != nil {
			route.Fail(w, r, err)
			return
		}
	}
	ok(w, http.StatusOK)
}

// checkRev refuses a stale revision of pkg with 409. A package without versions passes: the
// first step of an unpublish removed them already.
func (h *Handler) checkRev(ctx context.Context, repoID int64, pkg, rev string) error {
	p, err := h.q.GetPackage(ctx, dbq.GetPackageParams{RepoID: repoID, Name: pkg})
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return err
	case p.Rev != rev:
		return assets.Refuse(http.StatusConflict, "%s changed since it was read; run the command again", pkg)
	}
	return nil
}

// unpublishAll removes every version of pkg (DELETE <name>/-rev/<rev>).
func (h *Handler) unpublishAll(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, rev string) {
	ctx := r.Context()
	if _, err := h.q.GetPackage(ctx, dbq.GetPackageParams{RepoID: c.Repo.ID, Name: pkg}); isNoRows(err) {
		notFound(w, r)
		return
	}
	if err := h.checkRev(ctx, c.Repo.ID, pkg, rev); err != nil {
		route.Fail(w, r, err)
		return
	}
	dir, err := repopath.Folder(pkg + "/-")
	if err != nil {
		notFound(w, r)
		return
	}
	if _, err := h.assets.Remove(ctx, assets.Removal{Repo: c.Repo, Prefixes: []repopath.Path{dir}, Origin: format.OriginOf(c.P, assets.SourcePut)}); err != nil {
		route.Fail(w, r, err)
		return
	}
	ok(w, http.StatusOK)
}

// unpublishFile removes one tarball (DELETE <name>/-/<file>/-rev/<rev>), the last step of npm
// unpublish of a version; a tarball that the step before removed already answers 200.
func (h *Handler) unpublishFile(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, file, rev string) {
	ctx := r.Context()
	if err := h.checkRev(ctx, c.Repo.ID, pkg, rev); err != nil {
		route.Fail(w, r, err)
		return
	}
	p, err := repopath.Parse(pkg + "/-/" + file)
	if err != nil || !strings.HasSuffix(file, ".tgz") {
		notFound(w, r)
		return
	}
	if _, err := h.assets.Remove(ctx, assets.Removal{Repo: c.Repo, Paths: []repopath.Path{p}, Origin: format.OriginOf(c.P, assets.SourcePut)}); err != nil {
		route.Fail(w, r, err)
		return
	}
	ok(w, http.StatusOK)
}
