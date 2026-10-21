package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// Restored is what a restore wrote.
type Restored struct {
	Name     string
	Manifest *Manifest
	DataDir  string
	FSRoot   string // where the blobs went (fs)
	Blobs    int64  // blobs copied
}

// Restore restores the backup folder into dataDir, which must be empty or missing. It checks
// backup.json and the SHA-256 of the database, copies the blobs that the database uses from the
// shared blobs folder next to the backup into the blob store of the data dir (fs: TR_FS_ROOT, the
// stored fs.root, else blobs/ in the data dir), then master.key, and the database last.
func Restore(ctx context.Context, folder, dataDir string) (*Restored, error) {
	folder = filepath.Clean(folder)
	m, err := ReadManifest(folder)
	if err != nil {
		return nil, err
	}
	if err := checkEmpty(dataDir); err != nil {
		return nil, err
	}
	dbFile := filepath.Join(folder, DBFile)
	sum, _, err := hashFile(dbFile)
	if err != nil {
		return nil, err
	}
	if sum != m.Database.SHA256 {
		return nil, fmt.Errorf("%s does not match the SHA-256 in %s: the backup is damaged", DBFile, ManifestFile)
	}
	keyFile := filepath.Join(folder, config.MasterKeyFileName)
	var key []byte
	if m.MasterKeyIncluded {
		if key, err = os.ReadFile(keyFile); err != nil {
			return nil, fmt.Errorf("read the master key of the backup: %w", err)
		}
	}
	r := &Restored{Name: filepath.Base(folder), Manifest: m, DataDir: dataDir}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	if m.BlobsIncluded {
		if err := r.restoreBlobs(ctx, filepath.Dir(folder), dbFile); err != nil {
			return nil, err
		}
	}
	if key != nil {
		if err := writeSecret(config.JoinDataDir(dataDir, config.MasterKeyFileName), key); err != nil {
			return nil, err
		}
	}
	target := config.JoinDataDir(dataDir, DBFile)
	src, err := os.Open(dbFile)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := copyBlob(target, src, m.Database.SHA256, m.Database.Size); err != nil {
		return nil, fmt.Errorf("copy the database: %w", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return nil, err
	}
	return r, nil
}

// checkEmpty refuses a data dir that holds anything.
func checkEmpty(dir string) error {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(des) > 0 {
		return fmt.Errorf("the data folder %s is not empty; restore into an empty folder", dir)
	}
	return nil
}

// restoreBlobs copies every blob that the database of the backup uses from the blobs folder of
// dir into the fs store of the data dir. It checks first that the folder holds all of them.
func (r *Restored) restoreBlobs(ctx context.Context, dir, dbFile string) error {
	d, err := db.OpenReadOnly(dbFile)
	if err != nil {
		return err
	}
	defer d.Close()
	r.FSRoot, err = fsRoot(ctx, d, r.DataDir)
	if err != nil {
		return err
	}
	var missing []string
	err = eachBlob(ctx, d, func(b dbq.WalkReferencedBlobsRow) error {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(blobs.Key(b.Sha256))))
		if err != nil || st.Size() != b.Size {
			missing = append(missing, b.Sha256)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("the blobs folder %s lacks %d of the files that the backup uses (first: %s)",
			filepath.Join(dir, BlobsDir), len(missing), missing[0])
	}
	return eachBlob(ctx, d, func(b dbq.WalkReferencedBlobsRow) error {
		key := filepath.FromSlash(blobs.Key(b.Sha256))
		src, err := os.Open(filepath.Join(dir, key))
		if err != nil {
			return err
		}
		defer src.Close()
		if err := copyBlob(filepath.Join(r.FSRoot, key), src, b.Sha256, b.Size); err != nil {
			return err
		}
		r.Blobs++
		return nil
	})
}

// eachBlob calls fn for every blob that an asset of database d uses, in id order.
func eachBlob(ctx context.Context, d *sql.DB, fn func(dbq.WalkReferencedBlobsRow) error) error {
	var after int64
	for {
		page, err := dbq.New(d).WalkReferencedBlobs(ctx, dbq.WalkReferencedBlobsParams{AfterID: after, Limit: pageSize})
		if err != nil {
			return err
		}
		for _, b := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(b); err != nil {
				return err
			}
		}
		if len(page) < pageSize {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

// fsRoot is the folder of the fs store for the database d in dataDir, as the server opens it:
// TR_FS_ROOT, else the stored fs.root, else blobs/ in the data dir.
func fsRoot(ctx context.Context, d *sql.DB, dataDir string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(storage.FSRoot.Def().Env())); v != "" {
		return v, nil
	}
	v, err := storedString(ctx, d, storage.FSRoot.Name())
	if err != nil || v != "" {
		return v, err
	}
	return config.JoinDataDir(dataDir, storage.FSRoot.Def().DataDefault), nil
}

// storedString returns the stored value of the string setting key of database d, or "".
func storedString(ctx context.Context, d *sql.DB, key string) (string, error) {
	raw, err := dbq.New(d).GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return "", fmt.Errorf("setting %s: %w", key, err)
	}
	return strings.TrimSpace(s), nil
}
