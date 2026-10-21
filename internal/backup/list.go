package backup

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Entry is one backup folder of a backup dir.
type Entry struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Complete: backup.json is there and reads. An incomplete folder is a backup that did not
	// finish; the next scheduled run removes it.
	Complete bool      `json:"complete"`
	Manifest *Manifest `json:"-"`
}

// ReadManifest reads backup.json of the backup folder.
func ReadManifest(folder string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(folder, ManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s: it is not a backup, or the backup did not finish", folder, ManifestFile)
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if m.Format != Format {
		return nil, fmt.Errorf("%s has format %d; this server reads format %d", ManifestFile, m.Format, Format)
	}
	return &m, nil
}

// List returns the backup folders of dir, newest first. A missing dir has none.
func List(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		if !de.IsDir() || !nameRE.MatchString(de.Name()) {
			continue
		}
		at, err := time.Parse(nameLayout, de.Name())
		if err != nil {
			continue
		}
		e := Entry{Name: de.Name(), CreatedAt: at}
		if m, err := ReadManifest(filepath.Join(dir, de.Name())); err == nil {
			e.Complete, e.Manifest, e.CreatedAt = true, m, m.CreatedAt
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(b.Name, a.Name) })
	return out, nil
}

// Pruned is what a retention run removed.
type Pruned struct {
	Backups []string // folders: the complete ones past keep, and the incomplete ones past graceAge
	Blobs   int64    // files of the blobs folder that no kept backup uses
}

// graceAge is how old a file must be before retention removes it, when no kept backup accounts
// for it: an incomplete backup folder (by its name and its mtime), a file of the blobs folder and a
// temp file there. Another process may be writing it: a backup from the command line into the
// folder of the server's backups.
const graceAge = time.Hour

// Prune keeps the newest keep complete backups of dir and removes the other backup folders, the
// incomplete ones too unless their name and their mtime are both younger than graceAge. Then it
// removes each file of the blobs folder that no kept backup uses and whose mtime is older than
// graceAge: it reads the blob list of each kept backup's database. When a kept database cannot be
// read, it removes no blob.
func Prune(ctx context.Context, dir string, keep int) (Pruned, error) {
	var p Pruned
	entries, err := List(dir)
	if err != nil {
		return p, err
	}
	var kept []Entry
	for _, e := range entries {
		if e.Complete && len(kept) < keep {
			kept = append(kept, e)
			continue
		}
		if !e.Complete && young(e.CreatedAt) {
			if st, err := os.Stat(filepath.Join(dir, e.Name)); err == nil && young(st.ModTime()) {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name)); err != nil {
			return p, err
		}
		p.Backups = append(p.Backups, e.Name)
	}

	used := map[[32]byte]struct{}{}
	for _, e := range kept {
		if !e.Manifest.BlobsIncluded {
			continue
		}
		if err := usedBlobs(ctx, filepath.Join(dir, e.Name, DBFile), used); err != nil {
			return p, fmt.Errorf("read the blobs of backup %s: %w", e.Name, err)
		}
	}
	root := filepath.Join(dir, BlobsDir, "sha256")
	err = filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return ctx.Err()
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if young(info.ModTime()) {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, tmpPrefix) {
			return os.Remove(file)
		}
		var sum [32]byte
		if n, err := hex.Decode(sum[:], []byte(name)); err == nil && n == 32 {
			if _, ok := used[sum]; ok {
				return nil
			}
		}
		if err := os.Remove(file); err != nil {
			return err
		}
		p.Blobs++
		return nil
	})
	return p, err
}

// young reports whether t is less than graceAge ago.
func young(t time.Time) bool { return time.Since(t) < graceAge }

// usedBlobs adds the SHA-256 of every blob that an asset of the database file uses to used.
func usedBlobs(ctx context.Context, file string, used map[[32]byte]struct{}) error {
	d, err := db.OpenReadOnly(file)
	if err != nil {
		return err
	}
	defer d.Close()
	var after int64
	for {
		page, err := dbq.New(d).WalkReferencedBlobs(ctx, dbq.WalkReferencedBlobsParams{AfterID: after, Limit: pageSize})
		if err != nil {
			return err
		}
		for _, b := range page {
			var sum [32]byte
			if n, err := hex.Decode(sum[:], []byte(b.Sha256)); err != nil || n != 32 {
				return fmt.Errorf("blob row %d has no SHA-256", b.ID)
			}
			used[sum] = struct{}{}
		}
		if len(page) < pageSize {
			return nil
		}
		after = page[len(page)-1].ID
	}
}
