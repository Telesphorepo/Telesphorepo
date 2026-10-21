package assets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

var (
	// ErrUploadUnknown is an upload that does not exist, belongs to another repo or another
	// credential, or expired.
	ErrUploadUnknown = errors.New("upload unknown")
	// ErrDigest is a finished upload whose bytes do not have the digest or the size the client
	// gave. The upload is deleted.
	ErrDigest = errors.New("the upload does not match the given digest or size")
)

// RangeError is an append at an offset other than the bytes received so far.
type RangeError struct{ Received int64 }

func (e *RangeError) Error() string {
	return fmt.Sprintf("the upload has %d bytes; send the next bytes from that offset", e.Received)
}

// UploadSession is a resumable upload.
type UploadSession struct {
	ID        string    `json:"id"`
	Received  int64     `json:"received"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Uploads keeps resumable uploads: a client starts one, appends chunks at increasing offsets
// (across restarts: the bytes are in a file on the data volume), and finishes it with the digest
// of the whole body; Finish returns the body for a Commit. Nothing holds a whole body in memory.
// Only the credential that started an upload continues it: the same publish session, or the
// same user. An upload idle for upload.session_ttl expires.
type Uploads struct {
	db  *db.DB
	set settings.Provider

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewUploads returns the upload sessions.
func NewUploads(d *db.DB, set settings.Provider) *Uploads {
	return &Uploads{db: d, set: set, locks: map[string]*sync.Mutex{}}
}

func (u *Uploads) dir() string { return filepath.Join(u.set().TmpDir(), "uploads") }

func (u *Uploads) file(id string) string { return filepath.Join(u.dir(), id) }

// hashSuffix ends the name of the file next to an upload's bytes that holds the hash state of
// the bytes received (blobs.Hasher), so Finish needs no second read of the bytes.
const hashSuffix = ".hash"

func (u *Uploads) hashFile(id string) string { return u.file(id) + hashSuffix }

// hasher returns the hash state of the first received bytes of upload id, whose bytes are f: the
// saved state when it covers exactly those bytes, else the hashes of a read of them (after an
// append whose row update did not happen, or a damaged state file).
func (u *Uploads) hasher(id string, f *os.File, received int64) (*blobs.Hasher, error) {
	h := blobs.NewHasher()
	if b, err := os.ReadFile(u.hashFile(id)); err == nil && h.UnmarshalBinary(b) == nil && h.Size() == received {
		return h, nil
	}
	h = blobs.NewHasher()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, received)); err != nil {
		return nil, err
	}
	if h.Size() != received {
		return nil, fmt.Errorf("upload %s has %d bytes, not %d", id, h.Size(), received)
	}
	return h, nil
}

// saveHasher writes the hash state of upload id through a temp file and a rename. hasher checks
// what it reads, so the write needs no sync.
func (u *Uploads) saveHasher(id string, h *blobs.Hasher) error {
	b, err := h.MarshalBinary()
	if err != nil {
		return err
	}
	tmp := u.hashFile(id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, u.hashFile(id)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// lock serializes the calls of one upload.
func (u *Uploads) lock(id string) func() {
	u.mu.Lock()
	m := u.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		u.locks[id] = m
	}
	u.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (u *Uploads) forget(id string) {
	u.mu.Lock()
	delete(u.locks, id)
	u.mu.Unlock()
}

// UploadOwner returns the user and the publish session of p in repo: a core bearer token counts with
// the publish session of its scope of the repo (auth.Principal.For).
func UploadOwner(p *auth.Principal, repo *repos.Repo) (user, session int64) {
	p = p.For(repo.Name)
	if p == nil {
		return 0, 0
	}
	if p.Is(auth.CredPublish) && p.Publish != nil {
		return p.UserID, p.Publish.ID
	}
	return p.UserID, 0
}

// Start begins an upload into repo for p.
func (u *Uploads) Start(ctx context.Context, p *auth.Principal, repo *repos.Repo) (*UploadSession, error) {
	var raw [16]byte
	rand.Read(raw[:])
	id := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(u.dir(), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(u.file(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	f.Close()
	user, session := UploadOwner(p, repo)
	now := time.Now()
	exp := now.Add(settings.UploadSessionTTL.Get(u.set()))
	err = dbq.New(u.db.W).InsertUploadSession(ctx, dbq.InsertUploadSessionParams{ID: id, RepoID: repo.ID,
		UserID:           sql.NullInt64{Int64: user, Valid: user != 0},
		PublishSessionID: sql.NullInt64{Int64: session, Valid: session != 0},
		CreatedAt:        now.Unix(), UpdatedAt: now.Unix(), ExpiresAt: exp.Unix()})
	if err != nil {
		os.Remove(u.file(id))
		return nil, err
	}
	return &UploadSession{ID: id, ExpiresAt: time.Unix(exp.Unix(), 0).UTC()}, nil
}

// row returns upload id of repo when p started it.
func (u *Uploads) row(ctx context.Context, id string, p *auth.Principal, repo *repos.Repo) (dbq.UploadSession, error) {
	r, err := dbq.New(u.db.R).GetUploadSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrUploadUnknown
	}
	if err != nil {
		return r, err
	}
	user, session := UploadOwner(p, repo)
	if r.RepoID != repo.ID || r.PublishSessionID.Int64 != session || (session == 0 && r.UserID.Int64 != user) ||
		r.ExpiresAt < time.Now().Unix() {
		return r, ErrUploadUnknown
	}
	return r, nil
}

// Status returns upload id of repo when p started it and it has not expired.
func (u *Uploads) Status(ctx context.Context, id string, p *auth.Principal, repo *repos.Repo) (*UploadSession, error) {
	r, err := u.row(ctx, id, p, repo)
	if err != nil {
		return nil, err
	}
	return &UploadSession{ID: r.ID, Received: r.Received, ExpiresAt: time.Unix(r.ExpiresAt, 0).UTC()}, nil
}

// Append adds the bytes of body at offset to upload id. offset must equal the bytes received so
// far (else a *RangeError); a negative offset appends at the end. The upload may not grow above max_upload_size (ErrTooLarge). It
// returns the bytes received after the append.
func (u *Uploads) Append(ctx context.Context, id string, p *auth.Principal, repo *repos.Repo, offset int64, body io.Reader) (int64, error) {
	unlock := u.lock(id)
	defer unlock()
	r, err := u.row(ctx, id, p, repo)
	if err != nil {
		return 0, err
	}
	if offset < 0 {
		offset = r.Received
	}
	if offset != r.Received {
		return r.Received, &RangeError{Received: r.Received}
	}
	f, err := os.OpenFile(u.file(id), os.O_RDWR, 0)
	if err != nil {
		return r.Received, err
	}
	defer f.Close()
	// Bytes past received are from an append that did not finish: they are dropped.
	if err := f.Truncate(r.Received); err != nil {
		return r.Received, err
	}
	h, err := u.hasher(id, f, r.Received)
	if err != nil {
		return r.Received, err
	}
	if _, err := f.Seek(r.Received, io.SeekStart); err != nil {
		return r.Received, err
	}
	max := settings.MaxUploadSize.Get(u.set())
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, max-r.Received+1))
	if err == nil && r.Received+n > max {
		err = ErrTooLarge
	}
	if err != nil {
		f.Truncate(r.Received)
		return r.Received, err
	}
	if err := f.Sync(); err != nil {
		return r.Received, err
	}
	if err := u.saveHasher(id, h); err != nil {
		return r.Received, err
	}
	now := time.Now()
	received := r.Received + n
	if err := dbq.New(u.db.W).SetUploadReceived(context.WithoutCancel(ctx), dbq.SetUploadReceivedParams{Received: received,
		UpdatedAt: now.Unix(), ExpiresAt: now.Add(settings.UploadSessionTTL.Get(u.set())).Unix(), ID: id}); err != nil {
		return r.Received, err
	}
	return received, nil
}

// Finish ends upload id: it takes the hashes of the bytes received from the state that the
// appends saved, and checks the digest sha256 ("" checks none; the web UI, which cannot hash a
// large file in parts, gives the size) and, when size >= 0, the size. It returns the body for a
// Commit; closing the body removes the file. A mismatch deletes the upload and returns ErrDigest.
func (u *Uploads) Finish(ctx context.Context, id string, p *auth.Principal, repo *repos.Repo, sha256 string, size int64) (*Spooled, error) {
	unlock := u.lock(id)
	defer unlock()
	r, err := u.row(ctx, id, p, repo)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(u.file(id), os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	// Bytes past received are from an append that did not finish.
	h, err := func() (*blobs.Hasher, error) {
		if err := f.Truncate(r.Received); err != nil {
			return nil, err
		}
		return u.hasher(id, f, r.Received)
	}()
	if err != nil {
		f.Close()
		return nil, err
	}
	sp := h.Spooled(f)
	if err := dbq.New(u.db.W).DeleteUploadSession(context.WithoutCancel(ctx), id); err != nil {
		sp.Close()
		return nil, err
	}
	u.forget(id)
	os.Remove(u.hashFile(id))
	if (sha256 != "" && sp.SHA256 != sha256) || (size >= 0 && sp.Size != size) {
		sp.Close()
		return nil, ErrDigest
	}
	return sp, nil
}

// Cancel deletes upload id and its bytes.
func (u *Uploads) Cancel(ctx context.Context, id string, p *auth.Principal, repo *repos.Repo) error {
	unlock := u.lock(id)
	defer unlock()
	if _, err := u.row(ctx, id, p, repo); err != nil {
		return err
	}
	return u.drop(ctx, id)
}

func (u *Uploads) drop(ctx context.Context, id string) error {
	if err := dbq.New(u.db.W).DeleteUploadSession(ctx, id); err != nil {
		return err
	}
	u.forget(id)
	if err := os.Remove(u.hashFile(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(u.file(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// expireBatch is how many expired uploads one round of Expire deletes.
const expireBatch = 100

// Expire deletes the uploads idle past upload.session_ttl and their files (the
// core.upload-sessions job). Then it sweeps the files without a row: a repo delete or a user
// delete removes the rows of their uploads, and a stop between a Finish and its Commit leaves
// the file.
func (u *Uploads) Expire(ctx context.Context) error {
	for {
		ids, err := dbq.New(u.db.R).ListExpiredUploads(ctx, dbq.ListExpiredUploadsParams{Now: time.Now().Unix(), Limit: expireBatch})
		if err != nil {
			return err
		}
		for _, id := range ids {
			unlock := u.lock(id)
			err := u.drop(ctx, id)
			unlock()
			if err != nil {
				return err
			}
		}
		if len(ids) < expireBatch {
			return u.sweep(ctx)
		}
	}
}

// sweep deletes each upload file that has no upload_sessions row and was not written for
// upload.session_ttl. A file of a running upload has a row, or (between its Start and the row
// insert) a recent modification time.
func (u *Uploads) sweep(ctx context.Context) error {
	entries, err := os.ReadDir(u.dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-settings.UploadSessionTTL.Get(u.set()))
	var errs []error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), hashSuffix)
		unlock := u.lock(id)
		_, err = dbq.New(u.db.R).GetUploadSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			err = os.Remove(filepath.Join(u.dir(), e.Name()))
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			u.forget(id)
		}
		unlock()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
