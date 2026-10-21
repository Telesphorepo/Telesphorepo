// Package backup writes and restores backups of a server. A backup is a folder
// <backup dir>/<UTC time>/ with telesphorepo.db (SQLite VACUUM INTO of the live database),
// master.key (when the data dir has one) and backup.json, written last: a folder without
// backup.json is incomplete. With fs storage the blobs go into <backup dir>/blobs/, one shared
// content-addressed folder for every backup (a blob never changes); with S3 storage the blobs
// stay in the bucket. The module "backup" runs it on a schedule and on demand; the server
// commands "backup" and "restore" run it from the command line.
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The files of a backup folder.
const (
	DBFile       = "telesphorepo.db"
	ManifestFile = "backup.json"
	BlobsDir     = "blobs"
)

// Format is the version of the layout of a backup folder and of backup.json.
const Format = 1

// nameLayout names a backup folder by its UTC start time, so the names sort by time.
const nameLayout = "20060102T150405.000Z"

var nameRE = regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z$`)

// pageSize is how many blob rows one page of a walk reads.
const pageSize = 500

// Manifest is backup.json. It holds no secret.
type Manifest struct {
	Format    int       `json:"format"`
	Version   string    `json:"version"` // of the server that wrote it
	CreatedAt time.Time `json:"created_at"`
	// Storage is the blob store of the server: "fs" or "s3".
	Storage string `json:"storage"`
	// S3 names the bucket that holds the blobs of an s3 server, without credentials.
	S3 *S3Location `json:"s3,omitempty"`
	// BlobsIncluded: the blobs that the database uses are in the shared blobs folder (fs).
	BlobsIncluded bool `json:"blobs_included"`
	// Migrations are the migrations that the database applied, by component and version.
	Migrations []db.Migration `json:"migrations"`
	Counts     Counts         `json:"counts"`
	Database   Database       `json:"database"`
	// MasterKeyIncluded: master.key is in the folder. MasterKeySource says where the server
	// had its key: "file" (master.key in the data dir) or "env" (TR_MASTER_KEY, never written).
	MasterKeyIncluded bool   `json:"master_key_included"`
	MasterKeySource   string `json:"master_key_source"`
}

// S3Location is where an s3 server keeps its blobs.
type S3Location struct {
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix,omitempty"`
}

// Counts are what the database of a backup holds.
type Counts struct {
	Repos     int64 `json:"repos"`
	Files     int64 `json:"files"`      // asset rows
	FileBytes int64 `json:"file_bytes"` // their sizes
	Blobs     int64 `json:"blobs"`      // blobs that an asset uses
	BlobBytes int64 `json:"blob_bytes"`
}

// Database describes the database file of a backup.
type Database struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Size is what a restore of the backup brings back: the database and the blobs it uses.
func (m *Manifest) Size() int64 { return m.Database.Size + m.Counts.BlobBytes }

// Source is what a backup reads.
type Source struct {
	// DB reads the live database.
	DB      *sql.DB
	DataDir string
	// MasterKeyEnv: the server takes its master key from TR_MASTER_KEY, so there is no file to copy.
	MasterKeyEnv bool
	Version      string
	// Storage is the blob store of the server. Only its location goes into backup.json.
	Storage storage.Config
	// Get opens a blob object by storage key (fs storage only).
	Get func(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error)
	// Hold, when set, takes the digest lock of a blob, so the blob GC waits, and reports whether
	// the blob row still exists. The server sets it; a backup from the command line has none.
	Hold func(ctx context.Context, sha256 string) (release func(), exists bool, err error)
	// Progress, when set, gets the progress of the run.
	Progress func(Progress)
}

// Progress is the state of a running backup.
type Progress struct {
	Step   string `json:"step"`   // "files", "database" or "cleanup"
	Blobs  int64  `json:"blobs"`  // blobs checked so far in this step
	Copied int64  `json:"copied"` // blobs copied so far in this run
}

// Result is a written backup.
type Result struct {
	Name     string
	Folder   string
	Manifest *Manifest
	Copied   int64 // blobs copied into the blobs folder
}

// Run writes one backup into dir. With fs storage it first copies the blobs that the live
// database uses, then writes the database copy, then copies the blobs that the copy uses and
// the first pass missed (uploads in between). backup.json comes last. On an error the folder is
// removed.
func Run(ctx context.Context, dir string, src Source) (*Result, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	name, folder, err := newFolder(dir)
	if err != nil {
		return nil, err
	}
	res, err := run(ctx, dir, folder, src)
	if err != nil {
		os.RemoveAll(folder)
		return nil, err
	}
	res.Name, res.Folder = name, folder
	return res, nil
}

// newFolder creates the folder of a new backup, named by the current UTC time.
func newFolder(dir string) (string, string, error) {
	for {
		name := time.Now().UTC().Format(nameLayout)
		folder := filepath.Join(dir, name)
		err := os.Mkdir(folder, 0o750)
		if errors.Is(err, fs.ErrExist) {
			time.Sleep(time.Millisecond)
			continue
		}
		return name, folder, err
	}
}

func run(ctx context.Context, dir, folder string, src Source) (*Result, error) {
	started := time.Now().UTC()
	withBlobs := src.Storage.Backend != "s3"
	res := &Result{}
	c := &copier{dir: dir, folder: folder, src: src, res: res}
	if withBlobs {
		c.step("files")
		if err := c.walk(ctx, src.DB, false); err != nil {
			return nil, err
		}
	}

	c.step("database")
	dbFile := filepath.Join(folder, DBFile)
	if err := createEmpty(dbFile); err != nil {
		return nil, err
	}
	if err := db.VacuumInto(ctx, src.DB, dbFile); err != nil {
		return nil, fmt.Errorf("copy the database: %w", err)
	}
	snap, err := db.OpenReadOnly(dbFile)
	if err != nil {
		return nil, err
	}
	defer snap.Close()

	m := &Manifest{Format: Format, Version: src.Version, CreatedAt: started, Storage: "fs", BlobsIncluded: withBlobs, MasterKeySource: "file"}
	if !withBlobs {
		m.Storage = "s3"
		m.S3 = &S3Location{Endpoint: src.Storage.S3Endpoint, Bucket: src.Storage.S3Bucket, Prefix: src.Storage.S3Prefix}
	}
	c.step("files")
	if err := c.walk(ctx, snap, withBlobs); err != nil {
		return nil, err
	}
	m.Counts.Blobs, m.Counts.BlobBytes = c.blobs, c.bytes
	q := dbq.New(snap)
	tot, err := q.CountRepoTotals(ctx)
	if err != nil {
		return nil, err
	}
	m.Counts.Repos, m.Counts.Files, m.Counts.FileBytes = tot.Repos, tot.Files, tot.Bytes
	applied, err := q.ListMigrations(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range applied {
		m.Migrations = append(m.Migrations, db.Migration{Component: a.Component, Version: int(a.Version), Name: a.Name, Checksum: a.Checksum})
	}
	if err := snap.Close(); err != nil {
		return nil, err
	}
	sum, size, err := hashFile(dbFile)
	if err != nil {
		return nil, err
	}
	m.Database = Database{File: DBFile, Size: size, SHA256: sum}

	if src.MasterKeyEnv {
		m.MasterKeySource = "env"
	} else {
		key, err := os.ReadFile(config.JoinDataDir(src.DataDir, config.MasterKeyFileName))
		switch {
		case err == nil:
			if err := writeSecret(filepath.Join(folder, config.MasterKeyFileName), key); err != nil {
				return nil, err
			}
			m.MasterKeyIncluded = true
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("read the master key: %w", err)
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(folder, ManifestFile), append(b, '\n'), 0o640); err != nil {
		return nil, err
	}
	res.Manifest = m
	return res, nil
}

// copier copies blobs into the blobs folder of a backup dir. It keeps the mtime of its backup folder
// and of each blob that the backup uses fresh, so a retention run of another process (the server's,
// next to a CLI backup into the same dir) leaves them alone however long the backup takes.
type copier struct {
	dir    string
	folder string
	src    Source
	res    *Result

	stepName     string
	checked      int64
	blobs, bytes int64 // the blobs of the last walk and their bytes
}

func (c *copier) step(name string) {
	c.stepName, c.checked = name, 0
	c.report()
}

func (c *copier) report() {
	now := time.Now()
	_ = os.Chtimes(c.folder, now, now)
	if c.src.Progress != nil {
		c.src.Progress(Progress{Step: c.stepName, Blobs: c.checked, Copied: c.res.Copied})
	}
}

// walk goes through the blobs that the assets of database q use and, with fs storage, copies each
// one that the blobs folder lacks. With required, a blob that is gone from the store fails the
// run: the database copy needs it. The first walk (the live database) skips such a blob, because
// the blob GC may have removed it since the page was read.
func (c *copier) walk(ctx context.Context, q *sql.DB, required bool) error {
	c.blobs, c.bytes = 0, 0
	withBlobs := c.src.Storage.Backend != "s3"
	var after int64
	for {
		page, err := dbq.New(q).WalkReferencedBlobs(ctx, dbq.WalkReferencedBlobsParams{AfterID: after, Limit: pageSize})
		if err != nil {
			return fmt.Errorf("read blobs: %w", err)
		}
		for _, b := range page {
			c.blobs++
			c.bytes += b.Size
			if withBlobs {
				if err := c.copyOne(ctx, b, required); err != nil {
					return err
				}
			}
			c.checked++
		}
		c.report()
		if len(page) < pageSize {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

// copyOne copies blob b into the blobs folder unless the folder holds it with its size.
func (c *copier) copyOne(ctx context.Context, b dbq.WalkReferencedBlobsRow, required bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := blobs.Key(b.Sha256)
	dst := filepath.Join(c.dir, filepath.FromSlash(key))
	if st, err := os.Stat(dst); err == nil && st.Size() == b.Size {
		now := time.Now()
		_ = os.Chtimes(dst, now, now)
		return nil
	}
	if c.src.Hold != nil {
		release, exists, err := c.src.Hold(ctx, b.Sha256)
		if err != nil {
			return err
		}
		defer release()
		if !exists {
			if required {
				return fmt.Errorf("blob %s is gone from the server", b.Sha256)
			}
			return nil
		}
	}
	body, _, err := c.src.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		if required {
			return fmt.Errorf("blob %s is missing in the store", b.Sha256)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read blob %s: %w", b.Sha256, err)
	}
	defer body.Close()
	if err := copyBlob(dst, body, b.Sha256, b.Size); err != nil {
		return err
	}
	c.res.Copied++
	return nil
}

// copyBlob writes body to dst through a temp file in the same folder, checking its size and
// SHA-256 before the rename.
func copyBlob(dst string, body io.Reader, sum string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), tmpPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), body)
	switch {
	case err != nil:
	case n != size:
		err = fmt.Errorf("content %s is %d bytes, not %d", sum, n, size)
	case hex.EncodeToString(h.Sum(nil)) != sum:
		err = fmt.Errorf("content %s does not match its SHA-256", sum)
	}
	if err == nil {
		err = f.Chmod(0o640)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	tmp = ""
	return nil
}

// tmpPrefix starts the name of a file that is being written.
const tmpPrefix = ".tmp-"

// createEmpty creates an empty owner-only file (VACUUM INTO fills an empty file).
func createEmpty(file string) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// writeSecret writes a new owner-only file, as the server writes master.key.
func writeSecret(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(file)
	}
	return err
}

// writeAtomic writes data to file through a temp file and a rename.
func writeAtomic(file string, data []byte, mode os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(file), tmpPrefix+rand.Text())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// hashFile returns the hex SHA-256 and the size of file.
func hashFile(file string) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
