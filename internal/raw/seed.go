package raw

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// seed inserts n synthetic file rows numbered from start (module.SeedFunc).
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	rows := make([]assets.SeedRow, 0, n)
	for i := start; i < start+n; i++ {
		p := fmt.Sprintf("files/d%03d/file-%d.bin", i%500, i)
		sum := sha256.Sum256([]byte(p))
		rows = append(rows, assets.SeedRow{Path: p, Size: 1024 + int64(i%65536), SHA256: hex.EncodeToString(sum[:]),
			ContentType: assets.FallbackContentType, Kind: assets.KindArtifact})
	}
	_, err := assets.SeedTx(ctx, tx, repo.ID, rows)
	return err
}
