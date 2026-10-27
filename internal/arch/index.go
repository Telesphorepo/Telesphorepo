package arch

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"slices"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/arch/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// rebuild builds the units of group grp of repo (format.Spec Rebuild): the index of an
// architecture (grp is the architecture), or the signatures of every package (groupSign).
func (h *Handler) rebuild(ctx context.Context, repo *repos.Repo, grp string, units []string) error {
	if grp == groupSign {
		return h.signAll(ctx, repo)
	}
	if !slices.Contains(architectures(repo), grp) {
		return h.dropArch(ctx, repo, grp)
	}
	if slices.Contains(units, unitFill) {
		if err := h.fill(ctx, repo, grp); err != nil {
			return err
		}
	}
	return h.buildIndex(ctx, repo, grp)
}

// entryBatch is how many entries one transaction of a fill or a drop writes.
const entryBatch = 1000

// dropArch removes the entries and the index files of an architecture that the config no longer
// lists.
func (h *Handler) dropArch(ctx context.Context, repo *repos.Repo, arch string) error {
	q := dbq.New(h.db.W)
	for {
		n, err := q.DeleteEntriesOfArch(ctx, dbq.DeleteEntriesOfArchParams{RepoID: repo.ID, Arch: arch, Limit: entryBatch})
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	_, err := h.assets.Remove(ctx, assets.Removal{Repo: repo, Prefixes: []repopath.Path{repopath.Path(arch + "/")}, Origin: generated})
	return err
}

// fill writes the entries of an architecture again from its packages and the any packages: the
// newest package of each name (the architecture's own on a tie). It merges the two runs of
// arch_packages_arch in name order, one name at a time, and writes the entries in batches.
func (h *Handler) fill(ctx context.Context, repo *repos.Repo, arch string) error {
	type pick struct {
		id   int64
		vkey []byte
		arch string
	}
	best := map[string]pick{}
	for _, a := range []string{arch, "any"} {
		for r, err := range h.q.StreamArch(ctx, repo.ID, a) {
			if err != nil {
				return err
			}
			if b, ok := best[r.Name]; !ok || newer(r.Vkey, a, b.vkey, b.arch) {
				best[r.Name] = pick{r.ID, r.Vkey, a}
			}
		}
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	slices.Sort(names)
	for len(names) > 0 {
		k := min(len(names), entryBatch)
		err := h.db.Tx(ctx, func(tx *sql.Tx) error {
			q := dbq.New(tx)
			for _, n := range names[:k] {
				if err := q.UpsertEntry(ctx, dbq.UpsertEntryParams{RepoID: repo.ID, Arch: arch, Name: n, PackageID: best[n].id}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		names = names[k:]
	}
	return nil
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
	f, err := os.CreateTemp(dir, "arch-index-*")
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

// archive is one index being written: a tar in one gzip stream.
type archive struct {
	out *tempOut
	zw  *gzip.Writer
	tw  *tar.Writer
}

func (h *Handler) newArchive() (*archive, error) {
	out, err := h.newTemp()
	if err != nil {
		return nil, err
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.DefaultCompression)
	return &archive{out: out, zw: zw, tw: tar.NewWriter(zw)}, nil
}

// entry writes the folder <name>-<version>/ with the files of files (name, content).
func (a *archive) entry(dir string, files ...[2]string) error {
	mt := time.Unix(0, 0)
	if err := a.tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mt}); err != nil {
		return err
	}
	for _, f := range files {
		if err := a.tw.WriteHeader(&tar.Header{Name: dir + "/" + f[0], Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(f[1])), ModTime: mt}); err != nil {
			return err
		}
		if _, err := io.WriteString(a.tw, f[1]); err != nil {
			return err
		}
	}
	return nil
}

func (a *archive) close() (*assets.Spooled, error) {
	if err := a.tw.Close(); err != nil {
		return nil, err
	}
	if err := a.zw.Close(); err != nil {
		return nil, err
	}
	return a.out.spooled()
}

// buildIndex writes the index of an architecture from its entries in name order: index.db (each
// package's desc) and index.files (desc and files), each a tar in one gzip stream, with their
// detached signatures, in one commit.
func (h *Handler) buildIndex(ctx context.Context, repo *repos.Repo, arch string) error {
	db, err := h.newArchive()
	if err != nil {
		return err
	}
	defer db.out.remove()
	files, err := h.newArchive()
	if err != nil {
		return err
	}
	defer files.out.remove()
	for r, err := range h.q.StreamEntries(ctx, repo.ID, arch) {
		if err != nil {
			return err
		}
		dir := r.Name + "-" + r.Version
		if err := db.entry(dir, [2]string{"desc", r.Desc}); err != nil {
			return err
		}
		if err := files.entry(dir, [2]string{"desc", r.Desc}, [2]string{"files", r.Files}); err != nil {
			return err
		}
	}
	dbSp, err := db.close()
	if err != nil {
		return err
	}
	filesSp, err := files.close()
	if err != nil {
		return err
	}
	base := arch + "/index"
	ws := []assets.Write{
		{Repo: repo, Path: repopath.Path(base + ".db"), Body: assets.Body{Spool: dbSp}, Origin: generated},
		{Repo: repo, Path: repopath.Path(base + ".files"), Body: assets.Body{Spool: filesSp}, Origin: generated},
	}
	if s := h.signer(repo); s != nil {
		for _, x := range []struct {
			path string
			sp   *assets.Spooled
		}{{base + ".db.sig", dbSp}, {base + ".files.sig", filesSp}} {
			var sig bytes.Buffer
			if err := s.DetachSignBinary(&sig, io.NewSectionReader(x.sp.File, 0, x.sp.Size)); err != nil {
				return err
			}
			ws = append(ws, assets.Write{Repo: repo, Path: repopath.Path(x.path), Body: assets.Body{Spool: blobs.Bytes(sig.Bytes())}, Origin: generated})
		}
	}
	_, err = h.assets.CommitAll(ctx, ws)
	return err
}

// signBatch is how many packages one commit of signAll signs.
const signBatch = 500

// signAll writes the signature of every package of repo again with its current key, from the
// SHA-256 state saved at the upload (no package is read), a commit per batch in the order of the
// UNIQUE index, then marks the index of every architecture, so they are signed again too.
func (h *Handler) signAll(ctx context.Context, repo *repos.Repo) error {
	s := h.signer(repo)
	if s != nil {
		var after dbq.SignPageParams
		for {
			after.RepoID, after.Limit = repo.ID, signBatch
			rows, err := h.q.SignPage(ctx, after)
			if err != nil {
				return err
			}
			var ws []assets.Write
			for _, r := range rows {
				sig := h.signState(repo, s, r.SigState)
				if sig == nil {
					continue
				}
				ws = append(ws, assets.Write{Repo: repo, Path: repopath.Path(r.Filename + ".sig"), Body: assets.Body{Spool: blobs.Bytes(sig)}, Origin: generated})
			}
			if _, err := h.assets.CommitAll(ctx, ws); err != nil {
				return err
			}
			if len(rows) < signBatch {
				break
			}
			last := rows[len(rows)-1]
			after.Name, after.Version, after.Arch = last.Name, last.Version, last.Arch
		}
	}
	return h.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, a := range architectures(repo) {
			if err := h.rebuilds.MarkTx(ctx, tx, repo.ID, a, unitDB); err != nil {
				return err
			}
		}
		return nil
	})
}
