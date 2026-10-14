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
	"os"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
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
	f, err := os.CreateTemp(h.cfg.TmpDir(), "deb-index-*")
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
	_, _, err := h.assets.Put(ctx, repo, p, sp, contentType, assets.KindIndex, 0)
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

type pkgRow struct {
	pkg, ver, control, filename, md5, sha1, sha256 string
	size                                           int64
}

// pkgCursor streams one architecture's rows in (package, version) order straight off the
// UNIQUE index, so no SQL sort is needed.
type pkgCursor struct {
	rows *sql.Rows
	cur  pkgRow
	ok   bool
}

func (h *Handler) openCursor(ctx context.Context, repoID int64, dist, comp, arch string) (*pkgCursor, error) {
	rows, err := h.db.R.QueryContext(ctx, `SELECT package, version, control, filename, size, md5, sha1, sha256
		FROM deb_packages
		WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ?
		ORDER BY package, version`, repoID, dist, comp, arch)
	if err != nil {
		return nil, err
	}
	c := &pkgCursor{rows: rows}
	return c, c.next()
}

func (c *pkgCursor) next() error {
	c.ok = c.rows.Next()
	if !c.ok {
		return c.rows.Err()
	}
	r := &c.cur
	return c.rows.Scan(&r.pkg, &r.ver, &r.control, &r.filename, &r.size, &r.md5, &r.sha1, &r.sha256)
}

func lessRow(a, b *pkgRow) bool {
	if a.pkg != b.pkg {
		return a.pkg < b.pkg
	}
	return a.ver < b.ver
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

	rel := comp + "/binary-" + arch + "/"
	now := time.Now().Unix()
	return h.db.Tx(ctx, func(tx *sql.Tx) error {
		for name, sp := range map[string]*assets.Spooled{"Packages": plainSp, "Packages.gz": gzSp} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO deb_index_files
				(repo_id, distribution, path, size, md5, sha256, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (repo_id, distribution, path) DO UPDATE SET
				size = excluded.size, md5 = excluded.md5, sha256 = excluded.sha256, updated_at = excluded.updated_at`,
				repo.ID, dist, rel+name, sp.Size, sp.MD5, sp.SHA256, now); err != nil {
				return err
			}
		}
		return nil
	})
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
			c.rows.Close()
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
		w.WriteString(r.control)
		if !strings.HasSuffix(r.control, "\n") {
			w.WriteString("\n")
		}
		fmt.Fprintf(w, "Filename: %s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\n",
			r.filename, r.size, r.md5, r.sha1, r.sha256)
		if err := pick.next(); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

type indexFile struct {
	path, md5, sha256 string
	size              int64
}

// buildRelease regenerates Release (and, when signing, Release.gpg and InRelease last)
// from deb_index_files only.
func (h *Handler) buildRelease(ctx context.Context, repo *repos.Repo, dist string) error {
	rows, err := h.db.R.QueryContext(ctx, `SELECT path, size, md5, sha256 FROM deb_index_files
		WHERE repo_id = ? AND distribution = ? ORDER BY path`, repo.ID, dist)
	if err != nil {
		return err
	}
	var files []indexFile
	var comps []string
	for rows.Next() {
		var f indexFile
		if err := rows.Scan(&f.path, &f.size, &f.md5, &f.sha256); err != nil {
			rows.Close()
			return err
		}
		files = append(files, f)
		if c, _, _ := strings.Cut(f.path, "/"); len(comps) == 0 || comps[len(comps)-1] != c {
			comps = append(comps, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: %s\nAcquire-By-Hash: yes\n",
		repo.Name, repo.Name, dist, dist, time.Now().UTC().Format(time.RFC1123Z),
		strings.Join(repo.Debian().Architectures, " "), strings.Join(comps, " "))
	b.WriteString("MD5Sum:\n")
	for _, f := range files {
		fmt.Fprintf(&b, " %s %16d %s\n", f.md5, f.size, f.path)
	}
	b.WriteString("SHA256:\n")
	for _, f := range files {
		fmt.Fprintf(&b, " %s %16d %s\n", f.sha256, f.size, f.path)
	}
	release := b.Bytes()

	base := path.Join("dists", dist)
	if err := h.putBytes(ctx, repo, base+"/Release", "text/plain; charset=utf-8", func(w io.Writer) error {
		_, err := w.Write(release)
		return err
	}); err != nil {
		return err
	}
	if h.signer == nil {
		return nil
	}
	if err := h.putBytes(ctx, repo, base+"/Release.gpg", "application/pgp-signature", func(w io.Writer) error {
		return h.signer.DetachSign(w, release)
	}); err != nil {
		return err
	}
	return h.putBytes(ctx, repo, base+"/InRelease", "text/plain; charset=utf-8", func(w io.Writer) error {
		return h.signer.ClearSign(w, release)
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
