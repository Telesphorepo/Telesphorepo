package cargo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/cargo/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// seed inserts n synthetic crate versions numbered from start, each with its asset row
// (module.SeedFunc): 1000 crates with a version per thousand. A version that exists is kept.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		crate, vers := fmt.Sprintf("seedcrate%d", i%1000), fmt.Sprintf("%d.0.0", i/1000)
		p := cratePath(crate, vers)
		sum := sha256.Sum256([]byte(p))
		cksum := hex.EncodeToString(sum[:])
		rows, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: p, Size: 4096 + int64(i%65536), SHA256: cksum,
			ContentType: crateContentType, Kind: assets.KindArtifact}})
		if err != nil {
			return err
		}
		v, _ := version.ParseSemVer(vers)
		e := &entry{Name: crate, Vers: vers, Cksum: cksum}
		e.finish()
		line, err := e.line()
		if err != nil {
			return err
		}
		if err := q.SeedVersion(ctx, dbq.SeedVersionParams{RepoID: repo.ID, Name: crate, LowerName: crate, Version: vers,
			SortKey: v.SortKey(), BaseKey: baseKey(v), Entry: line, AssetID: rows[0].ID, Path: p, CreatedAt: now}); err != nil {
			return err
		}
		if err := q.InsertName(ctx, dbq.InsertNameParams{RepoID: repo.ID, FoldedName: fold(crate), Name: crate}); err != nil {
			return err
		}
	}
	return nil
}
