package rpm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

const (
	repomdPath = "repodata/repomd.xml"
	repomdSig  = "repodata/repomd.xml.asc"
)

// rebuild builds the units of group grp of repo (format.Spec Rebuild): the repodata, or the
// signature of every package (groupSign).
func (h *Handler) rebuild(ctx context.Context, repo *repos.Repo, grp string, _ []string) error {
	if grp == groupSign {
		return h.signAll(ctx, repo)
	}
	return h.buildRepodata(ctx, repo)
}

// stream is one repodata file being written: the XML through gzip into a temp file, with the
// SHA-256 and the size of the XML (the open checksum) beside the hashes of the file.
type stream struct {
	typ   string
	out   *tempOut
	zw    *gzip.Writer
	open  hash.Hash
	openN int64
	w     io.Writer
	entry func(io.Writer, *indexRow) error
}

func (s *stream) Write(p []byte) (int, error) {
	s.open.Write(p)
	s.openN += int64(len(p))
	return s.zw.Write(p)
}

func (h *Handler) newStream(typ string, entry func(io.Writer, *indexRow) error) (*stream, error) {
	out, err := h.newTemp()
	if err != nil {
		return nil, err
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.DefaultCompression)
	s := &stream{typ: typ, out: out, zw: zw, open: sha256.New(), entry: entry}
	s.w = bufio.NewWriterSize(s, 64<<10)
	return s, nil
}

// buildRepodata writes primary, filelists and other from the packages of repo in name and
// version order (rpm_packages_order), each one gzip stream, then repomd.xml and its signature,
// in one commit. It keeps the data files of the previous repomd.xml, so a client that read it
// still finds them, and removes older ones. The cost is the packages of the repo, once per
// debounced batch; a request for the repodata reads a stored file.
func (h *Handler) buildRepodata(ctx context.Context, repo *repos.Repo) error {
	count, err := h.q.CountPackages(ctx, repo.ID)
	if err != nil {
		return err
	}
	ss := make([]*stream, 0, 3)
	defer func() {
		for _, s := range ss {
			s.out.remove()
		}
	}()
	for _, x := range []struct {
		typ   string
		head  string
		entry func(io.Writer, *indexRow) error
	}{
		{"primary", primaryHead(int(count)), writePrimary},
		{"filelists", filelistsHead(int(count)), writeFilelists},
		{"other", otherHead(int(count)), writeOther},
	} {
		s, err := h.newStream(x.typ, x.entry)
		if err != nil {
			return err
		}
		ss = append(ss, s)
		if _, err := io.WriteString(s.w, x.head); err != nil {
			return err
		}
	}
	for r, err := range h.q.StreamIndex(ctx, repo.ID) {
		if err != nil {
			return err
		}
		row := rowOf(r)
		for _, s := range ss {
			if err := s.entry(s.w, row); err != nil {
				return err
			}
		}
	}
	now := time.Now().Unix()
	var ds []repomdData
	var ws []assets.Write
	for i, tail := range []string{primaryTail, filelistsTail, otherTail} {
		s := ss[i]
		if _, err := io.WriteString(s.w, tail); err != nil {
			return err
		}
		if err := s.w.(*bufio.Writer).Flush(); err != nil {
			return err
		}
		if err := s.zw.Close(); err != nil {
			return err
		}
		sp, err := s.out.spooled()
		if err != nil {
			return err
		}
		href := "repodata/" + sp.SHA256 + "-" + s.typ + ".xml.gz"
		ds = append(ds, repomdData{Type: s.typ, Checksum: sp.SHA256, OpenChecksum: hex.EncodeToString(s.open.Sum(nil)),
			Href: href, Timestamp: now, Size: sp.Size, OpenSize: s.openN})
		ws = append(ws, assets.Write{Repo: repo, Path: repopath.Path(href), Body: assets.Body{Spool: sp}, Origin: generated})
	}
	md := repomdXML(now, ds)
	ws = append(ws, assets.Write{Repo: repo, Path: repomdPath, Body: assets.Body{Spool: blobs.Bytes(md)}, Origin: generated})
	if s := h.signer(repo); s != nil {
		var sig bytes.Buffer
		if err := s.DetachSign(&sig, md); err != nil {
			return err
		}
		ws = append(ws, assets.Write{Repo: repo, Path: repomdSig, Body: assets.Body{Spool: blobs.Bytes(sig.Bytes())}, Origin: generated})
	}
	prev, err := h.q.GetRepodata(ctx, repo.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cur := make([]string, len(ds))
	for i, d := range ds {
		cur[i] = d.Href
	}
	// The new names are recorded before the commit, so a delete of a current file builds the
	// repodata again (onRemoved) and the cleanup below does not.
	if err := dbq.New(h.db.W).SetRepodata(ctx, dbq.SetRepodataParams{RepoID: repo.ID, Hrefs: strings.Join(cur, " ")}); err != nil {
		return err
	}
	if _, err := h.assets.CommitAll(ctx, ws); err != nil {
		return err
	}
	keep := append(cur, strings.Fields(prev)...)
	return h.cleanRepodata(ctx, repo, append(keep, repomdPath, repomdSig))
}

// rowOf is the index row of a stored package.
func rowOf(r *dbq.IndexRow) *indexRow {
	return &indexRow{Name: r.Name, Arch: r.Arch, Version: r.Version, Release: r.Release, Epoch: r.Epoch,
		SHA256: r.Sha256, Size: r.Size, FileTime: r.CreatedAt, BuildTime: r.BuildTime,
		InstalledSize: r.InstalledSize, ArchiveSize: r.ArchiveSize, Location: r.Filename,
		HeaderStart: r.HeaderStart, HeaderEnd: r.HeaderStart + r.HeaderLen,
		parts: parts{Text: r.Text, FormatA: r.FormatA, FormatB: r.FormatB, Files: r.Files, Other: r.Other}}
}

// cleanRepodata removes the files of repodata/ that keep does not name: the data files of older
// generations. A signature stays: without a usable key the repo keeps its last one, which no
// longer matches, so dnf refuses the repodata as it would an unsigned one.
func (h *Handler) cleanRepodata(ctx context.Context, repo *repos.Repo, keep []string) error {
	var gone []repopath.Path
	after := ""
	for {
		page, next, err := h.assets.List(ctx, repo.ID, "repodata/", after, 500)
		if err != nil {
			return err
		}
		for _, a := range page {
			if !slices.Contains(keep, a.Path) {
				gone = append(gone, repopath.Path(a.Path))
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	if len(gone) == 0 {
		return nil
	}
	_, err := h.assets.Remove(ctx, assets.Removal{Repo: repo, Paths: gone, Origin: generated})
	return err
}

// Re-signing works through the packages in batches: signBatch packages or signBatchBytes bytes
// a commit.
const (
	signBatch      = 500
	signBatchBytes = 1 << 30
)

// signAll writes every package of repo again with a signature by its current key: the signature
// comes from the SHA-256 state of the header saved at the upload, and the file is the stored file
// with a new signature header (the header and the payload are copied), a commit per batch in the
// order of rpm_packages_order. Then it marks the repodata, whose checksums changed. A package
// that a delete or another write changed since its page was read is left as it is (admit).
func (h *Handler) signAll(ctx context.Context, repo *repos.Repo) error {
	s := h.signer(repo)
	if s != nil {
		var name string
		var vkey []byte
		var id int64
		for {
			rows, err := h.q.SignPage(ctx, repo.ID, name, vkey, id, signBatch)
			if err != nil {
				return err
			}
			var ws []assets.Write
			var size int64
			flush := func() error {
				defer func() {
					for _, w := range ws {
						w.Body.Spool.Close()
					}
					ws, size = nil, 0
				}()
				if len(ws) == 0 {
					return nil
				}
				_, err := h.assets.CommitAll(ctx, ws)
				return err
			}
			for _, r := range rows {
				w, err := h.resigned(ctx, repo, r, s)
				if err != nil {
					flush()
					return err
				}
				if w == nil {
					continue
				}
				ws = append(ws, *w)
				if size += w.Body.Spool.Size; size >= signBatchBytes {
					if err := flush(); err != nil {
						return err
					}
				}
			}
			if err := flush(); err != nil {
				return err
			}
			if len(rows) < signBatch {
				break
			}
			last := rows[len(rows)-1]
			name, vkey, id = last.Name, last.Vkey, last.ID
		}
	}
	return h.db.Tx(ctx, func(tx *sql.Tx) error {
		return h.rebuilds.MarkTx(ctx, tx, repo.ID, groupRepodata, unitIndex)
	})
}

// resigned returns the write of package r signed again by the signer, or nil when its file is
// gone.
func (h *Handler) resigned(ctx context.Context, repo *repos.Repo, r dbq.SignRow, s *signing.Signer) (*assets.Write, error) {
	a, err := h.assets.Get(ctx, repo.ID, r.Filename)
	if errors.Is(err, assets.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	rc, err := h.assets.Open(ctx, a)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 64<<10)
	lead := make([]byte, leadSize)
	if _, err := io.ReadFull(br, lead); err != nil {
		return nil, err
	}
	sig, sigRaw, err := readHeader(br, maxSigDataSize)
	if err != nil {
		return nil, err
	}
	pad := pad8(len(sigRaw))
	if int64(leadSize+len(sigRaw)+pad) != r.HeaderStart {
		return nil, errors.New("the stored package " + r.Filename + " does not match its row")
	}
	if _, err := io.CopyN(io.Discard, br, int64(pad)); err != nil {
		return nil, err
	}
	sigh, err := signedSigHeader(sig, int(r.Format), h.signState(repo, s, r.HdrState))
	if err != nil {
		return nil, err
	}
	out, err := h.newTemp()
	if err != nil {
		return nil, err
	}
	out.Write(lead)
	out.Write(sigh)
	if _, err := io.Copy(out, br); err != nil {
		out.remove()
		return nil, err
	}
	sp, err := out.spooled()
	if err != nil {
		out.remove()
		return nil, err
	}
	return &assets.Write{Repo: repo, Path: repopath.Path(r.Filename), Body: assets.Body{Spool: sp},
		ContentType: rpmContentType, Kind: assets.KindArtifact, Origin: generated,
		Meta: &resignMeta{id: r.ID, headerStart: int64(leadSize + len(sigh)), from: a.SHA256}}, nil
}
