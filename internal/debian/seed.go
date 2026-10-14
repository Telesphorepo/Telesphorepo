package debian

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	coredbq "github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Seed inserts n synthetic packages numbered from start, each with its asset row
// (format.Seeder). A package that already exists is kept.
func (h *Handler) Seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := coredbq.New(tx)
	pkg, err := tx.PrepareContext(ctx, `INSERT INTO deb_packages
		(repo_id, distribution, component, architecture, package, version, asset_id, filename, size, md5, sha1, sha256, control, created_at)
		VALUES (?, 'stable', 'main', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, distribution, component, architecture, package, version) DO NOTHING`)
	if err != nil {
		return err
	}
	defer pkg.Close()
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		name, version, arch := fmt.Sprintf("seedpkg%d", i), fmt.Sprintf("1.0.%d", i), "amd64"
		if i%2 == 1 {
			arch = "arm64"
		}
		filename := fmt.Sprintf("pool/main/s/%s/%s_%s_%s.deb", name, name, version, arch)
		s256 := sha256.Sum256([]byte(filename))
		s1 := sha1.Sum([]byte(filename))
		m5 := md5.Sum([]byte(filename))
		sum := hex.EncodeToString(s256[:])
		size := 2048 + int64(i%65536)
		a, err := q.UpsertAsset(ctx, coredbq.UpsertAssetParams{
			RepoID: repo.ID, Path: filename, Size: size, Sha256: sum,
			ContentType: "application/vnd.debian.binary-package", Kind: "artifact", CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Seed <seed@example.com>\nDescription: synthetic package %d\n", name, version, arch, i)
		if _, err := pkg.ExecContext(ctx, repo.ID, arch, name, version, a.ID, filename, size,
			hex.EncodeToString(m5[:]), hex.EncodeToString(s1[:]), sum, control, now); err != nil {
			return err
		}
	}
	return nil
}
