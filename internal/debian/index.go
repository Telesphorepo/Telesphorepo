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
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// tempOut is a buffered temp file that hashes everything written to it.
type tempOut struct {
	f                 *os.File
	bw                *bufio.Writer
	md5, sha1, sha256 hash.Hash
	n                 int64
}

func (h *Handler) newTemp() (*tempOut, error) {
	f, err := os.CreateTemp(h.cfg().TmpDir(), "deb-index-*")
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

// put stores an index file (kind "index") under the per-path asset lock.
func (h *Handler) put(ctx context.Context, repo *repos.Repo, p string, sp *assets.Spooled, contentType string) error {
	unlock := h.assets.Lock(repo.ID, p)
	defer unlock()
	if _, err := sp.File.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, _, err := h.assets.Put(ctx, repo, p, sp, contentType, assets.KindIndex, assets.By{})
	return err
}

// putByHash stores an immutable by-hash copy unless it already exists.
func (h *Handler) putByHash(ctx context.Context, repo *repos.Repo, dir string, sp *assets.Spooled) error {
	p := dir + "/by-hash/SHA256/" + sp.SHA256
	if a, err := h.assets.Get(ctx, repo.ID, p); err == nil && a.SHA256 == sp.SHA256 {
		return nil
	} else if err != nil && !errors.Is(err, assets.ErrNotFound) {
		return err
	}
	return h.put(ctx, repo, p, sp, "application/octet-stream")
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

// buildPackages writes Packages and Packages.gz for (dist, comp, arch) — rows of arch
// plus "all", merged in (package, version) order — and stores them by-hash first.
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

	dir := "dists/" + dist + "/" + comp + "/binary-" + arch
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
	if err := h.putByHash(ctx, repo, dir, plainSp); err != nil {
		return err
	}
	if err := h.putByHash(ctx, repo, dir, gzSp); err != nil {
		return err
	}
	if err := h.put(ctx, repo, dir+"/Packages", plainSp, "text/plain; charset=utf-8"); err != nil {
		return err
	}
	if err := h.put(ctx, repo, dir+"/Packages.gz", gzSp, "application/gzip"); err != nil {
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
	return h.pruneByHash(ctx, repo, dir, keep)
}

func (h *Handler) writePackages(ctx context.Context, repoID int64, dist, comp, arch string, plain, gzOut io.Writer) error {
	zw, err := gzip.NewWriterLevel(gzOut, gzip.BestCompression)
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

// buildRelease regenerates Release (and, when signing, Release.gpg and InRelease last)
// from deb_index_files only.
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

	var b bytes.Buffer
	fmt.Fprintf(&b, "Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: %s\nAcquire-By-Hash: yes\n",
		repo.Name, repo.Name, dist, dist, time.Now().UTC().Format(time.RFC1123Z),
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

	base := path.Join("dists", dist)
	if err := h.putBytes(ctx, repo, base+"/Release", "text/plain; charset=utf-8", func(w io.Writer) error {
		_, err := w.Write(release)
		return err
	}); err != nil {
		return err
	}
	signer := h.keys.Signer(signingKey(repo))
	if signer == nil {
		h.log.Error("the repo's signing key does not exist or is unusable; its Release is unsigned",
			"repo", repo.Name, "signing_key", signingKey(repo))
		return nil
	}
	if err := h.putBytes(ctx, repo, base+"/Release.gpg", "application/pgp-signature", func(w io.Writer) error {
		return signer.DetachSign(w, release)
	}); err != nil {
		return err
	}
	return h.putBytes(ctx, repo, base+"/InRelease", "text/plain; charset=utf-8", func(w io.Writer) error {
		return signer.ClearSign(w, release)
	})
}

func (h *Handler) putBytes(ctx context.Context, repo *repos.Repo, p, contentType string, write func(io.Writer) error) error {
	t, err := h.newTemp()
	if err != nil {
		return err
	}
	defer t.remove()
	if err := write(t); err != nil {
		return err
	}
	sp, err := t.spooled()
	if err != nil {
		return err
	}
	return h.put(ctx, repo, p, sp, contentType)
}
