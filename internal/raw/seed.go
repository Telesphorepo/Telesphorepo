package raw

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Seed inserts n synthetic file assets numbered from start (format.Seeder).
func (f *Format) Seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		p := fmt.Sprintf("files/d%03d/file-%d.bin", i%500, i)
		sum := sha256.Sum256([]byte(p))
		_, err := q.UpsertAsset(ctx, dbq.UpsertAssetParams{
			RepoID: repo.ID, Path: p, Size: 1024 + int64(i%65536), Sha256: hex.EncodeToString(sum[:]),
			ContentType: "application/octet-stream", Kind: "artifact", CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
