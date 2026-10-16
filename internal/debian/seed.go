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

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// seed inserts n synthetic packages numbered from start, each with its asset row
// (module.SeedFunc). A package that already exists is kept.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
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
		rows, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: filename, Size: size, SHA256: sum,
			ContentType: debContentType, Kind: assets.KindArtifact}})
		if err != nil {
			return err
		}
		control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Seed <seed@example.com>\nDescription: synthetic package %d\n", name, version, arch, i)
		if _, err := pkg.ExecContext(ctx, repo.ID, arch, name, version, rows[0].ID, filename, size,
			hex.EncodeToString(m5[:]), hex.EncodeToString(s1[:]), sum, control, now); err != nil {
			return err
		}
	}
	return nil
}
