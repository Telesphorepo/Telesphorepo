package debian

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"iter"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// generated is the origin of the index files: the server builds them.
var generated = assets.Origin{Source: assets.SourceGenerated}

// rebuild builds the units of distribution dist of repo (format.Spec Rebuild): the Packages
// files of each configured architecture, the removal of the index files of an architecture the
// config no longer lists, then the Release files of the distribution.
func (h *Handler) rebuild(ctx context.Context, repo *repos.Repo, dist string, units []string) error {
	archs := architectures(repo)
	for _, u := range units {
		comp, arch, ok := splitUnit(u)
		if !ok {
			continue
		}
		if !slices.Contains(archs, arch) {
			if err := h.dropUnit(ctx, repo, dist, comp, arch); err != nil {
				return err
			}
			continue
		}
		if err := h.buildPackages(ctx, repo, dist, comp, arch); err != nil {
			return err
		}
	}
	return h.buildRelease(ctx, repo, dist)
}

// dropUnit removes the index files of (dist, comp, arch) and their rows in deb_index_files, so
// they are no longer served or listed in Release.
func (h *Handler) dropUnit(ctx context.Context, repo *repos.Repo, dist, comp, arch string) error {
	dir := indexDir(dist, comp, arch)
	rel := strings.TrimPrefix(dir, "dists/"+dist+"/")
	err := h.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		files, err := q.ListIndexFiles(ctx, dbq.ListIndexFilesParams{RepoID: repo.ID, Distribution: dist})
		if err != nil {
			return err
		}
		for _, f := range files {
			if strings.HasPrefix(f.Path, rel) {
				if err := q.DeleteIndexFile(ctx, dbq.DeleteIndexFileParams{RepoID: repo.ID, Distribution: dist, Path: f.Path}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, err = h.assets.Remove(ctx, assets.Removal{Repo: repo, Prefixes: []repopath.Path{repopath.Path(dir)}, Origin: generated})
	return err
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
	f, err := os.CreateTemp(dir, "deb-index-*")
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

// pkgCursor pulls one architecture's rows in (package, version) order from
// dbq.StreamPackages.
type pkgCursor struct {
	pull func() (dbq.StreamPackagesRow, error, bool)
	stop func()
	cur  dbq.StreamPackagesRow
	ok   bool
}

func (h *Handler) openCursor(ctx context.Context, repoID int64, dist, comp, arch string) (*pkgCursor, error) {
	pull, stop := iter.Pull2(h.q.StreamPackages(ctx, dbq.StreamPackagesParams{
		RepoID: repoID, Distribution: dist, Component: comp, Architecture: arch}))
	c := &pkgCursor{pull: pull, stop: stop}
	return c, c.next()
}

func (c *pkgCursor) next() error {
	r, err, ok := c.pull()
	if err != nil {
		c.ok = false
		return err
	}
	c.cur, c.ok = r, ok
	return nil
}

func lessRow(a, b *dbq.StreamPackagesRow) bool {
	if a.Package != b.Package {
		return a.Package < b.Package
	}
	return a.Version < b.Version
}

// buildPackages writes Packages and Packages.gz for (dist, comp, arch) — rows of arch plus
// "all", merged in (package, version) order — in one commit with their by-hash copies, records
// them in deb_index_files, then deletes the by-hash files of older generations.
func (h *Handler) buildPackages(ctx context.Context, repo *repos.Repo, dist, comp, arch string) error {
	plain, err := h.newTemp()
	if err != nil {
		return err
	}
	defer plain.remove()
	gz, err := h.newTemp()
	if err != nil {
		return err
	}
	defer gz.remove()

	if err := h.writePackages(ctx, repo.ID, dist, comp, arch, plain, gz); err != nil {
		return err
	}
	plainSp, err := plain.spooled()
	if err != nil {
		return err
	}
	gzSp, err := gz.spooled()
	if err != nil {
		return err
	}

	dir := indexDir(dist, comp, arch)
	rel := comp + "/binary-" + arch + "/"
	// The generation this rebuild replaces: its by-hash files stay for clients mid-update.
	keep := []string{plainSp.SHA256, gzSp.SHA256}
	for _, file := range []string{"Packages", "Packages.gz"} {
		old, err := h.q.GetIndexFileSHA256(ctx, dbq.GetIndexFileSHA256Params{RepoID: repo.ID, Distribution: dist, Path: rel + file})
		switch {
		case err == nil:
			keep = append(keep, old)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	write := func(p string, sp *assets.Spooled) assets.Write {
		return assets.Write{Repo: repo, Path: repopath.Path(p), Body: assets.Body{Spool: sp}, Origin: generated}
	}
	byHash := dir + "by-hash/SHA256/"
	if _, err := h.assets.CommitAll(ctx, []assets.Write{
		write(byHash+plainSp.SHA256, plainSp), write(byHash+gzSp.SHA256, gzSp),
		write(dir+"Packages", plainSp), write(dir+"Packages.gz", gzSp),
	}); err != nil {
		return err
	}

	now := time.Now().Unix()
	err = h.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		for file, sp := range map[string]*assets.Spooled{"Packages": plainSp, "Packages.gz": gzSp} {
			if err := q.UpsertIndexFile(ctx, dbq.UpsertIndexFileParams{RepoID: repo.ID, Distribution: dist,
				Path: rel + file, Size: sp.Size, Md5: sp.MD5, Sha256: sp.SHA256, UpdatedAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return h.pruneByHash(ctx, repo, byHash, keep)
}

func (h *Handler) writePackages(ctx context.Context, repoID int64, dist, comp, arch string, plain, gzOut io.Writer) error {
	zw, err := gzip.NewWriterLevel(gzOut, gzip.DefaultCompression)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(io.MultiWriter(plain, zw), 64<<10)

	var cursors []*pkgCursor
	defer func() {
		for _, c := range cursors {
			c.stop()
		}
	}()
	for _, a := range []string{arch, "all"} {
		if a == "all" && arch == "all" {
			continue
		}
		c, err := h.openCursor(ctx, repoID, dist, comp, a)
		if c != nil {
			cursors = append(cursors, c)
		}
		if err != nil {
			return err
		}
	}

	first := true
	for {
		var pick *pkgCursor
		for _, c := range cursors {
			if c.ok && (pick == nil || lessRow(&c.cur, &pick.cur)) {
				pick = c
			}
		}
		if pick == nil {
			break
		}
		r := &pick.cur
		if !first {
			w.WriteString("\n")
		}
		first = false
		w.WriteString(r.Control)
		if !strings.HasSuffix(r.Control, "\n") {
			w.WriteString("\n")
		}
		fmt.Fprintf(w, "Filename: %s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\n",
			r.Filename, r.Size, r.Md5, r.Sha1, r.Sha256)
		if err := pick.next(); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

// buildRelease writes Release (and, when signing, Release.gpg and InRelease) from
// deb_index_files only, in one commit.
func (h *Handler) buildRelease(ctx context.Context, repo *repos.Repo, dist string) error {
	files, err := h.q.ListIndexFiles(ctx, dbq.ListIndexFilesParams{RepoID: repo.ID, Distribution: dist})
	if err != nil {
		return err
	}
	var comps []string
	for _, f := range files {
		if c, _, _ := strings.Cut(f.Path, "/"); len(comps) == 0 || comps[len(comps)-1] != c {
			comps = append(comps, c)
		}
	}

	base := "dists/" + dist + "/"
	origin, label, err := h.releaseNames(ctx, repo, base+"Release")
	if err != nil {
		return err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: %s\nAcquire-By-Hash: yes\n",
		origin, label, dist, dist, time.Now().UTC().Format(time.RFC1123Z),
		strings.Join(architectures(repo), " "), strings.Join(comps, " "))
	b.WriteString("MD5Sum:\n")
	for _, f := range files {
		fmt.Fprintf(&b, " %s %16d %s\n", f.Md5, f.Size, f.Path)
	}
	b.WriteString("SHA256:\n")
	for _, f := range files {
		fmt.Fprintf(&b, " %s %16d %s\n", f.Sha256, f.Size, f.Path)
	}
	release := b.Bytes()

	ws :=[]assets.Write{{Repo: repo, Path: repopath.Path(base + "Release"), Body: assets.Body{Spool: blobs.Bytes(release)}, Origin: generated}}
	signer := h.keys.Signer(signingKey(repo))
	if signer == nil {
		h.log.Error("the repo's signing key does not exist or is unusable; its Release is unsigned",
			"repo", repo.Name, "signing_key", signingKey(repo))
	} else {
		var detached, clear bytes.Buffer
		if err := signer.DetachSign(&detached, release); err != nil {
			return err
		}
		if err := signer.ClearSign(&clear, release); err != nil {
			return err
		}
		ws = append(ws,
			assets.Write{Repo: repo, Path: repopath.Path(base + "Release.gpg"), Body: assets.Body{Spool: blobs.Bytes(detached.Bytes())}, Origin: generated},
			assets.Write{Repo: repo, Path: repopath.Path(base + "InRelease"), Body: assets.Body{Spool: blobs.Bytes(clear.Bytes())}, Origin: generated})
	}
	_, err = h.assets.CommitAll(ctx, ws)
	return err
}

// releaseHead is how much of the current Release releaseNames reads: Origin and Label are its
// first lines.
const releaseHead = 4 << 10

// releaseNames returns the Origin and the Label of the Release at path: those of the Release
// that the repo has there, else the repo name. apt refuses the update of a source whose Release
// changed its Origin or Label, so a renamed repo keeps the ones its clients know.
func (h *Handler) releaseNames(ctx context.Context, repo *repos.Repo, path string) (string, string, error) {
	origin, label := repo.Name, repo.Name
	a, err := h.assets.Get(ctx, repo.ID, path)
	if errors.Is(err, assets.ErrNotFound) {
		return origin, label, nil
	}
	if err != nil {
		return "", "", err
	}
	rc, err := h.assets.Open(ctx, a)
	if err != nil {
		return "", "", err
	}
	defer rc.Close()
	sc := bufio.NewScanner(io.LimitReader(rc, releaseHead))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ": ")
		switch {
		case !ok || strings.HasPrefix(k, " "):
		case k == "Origin" && v != "":
			origin = v
		case k == "Label" && v != "":
			label = v
		}
	}
	return origin, label, nil
}