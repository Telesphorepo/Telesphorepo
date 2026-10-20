package nuget

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// seed inserts n synthetic package versions numbered from start, each with the asset row of its
// .nupkg (module.SeedFunc): 100 versions per package id.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		id := fmt.Sprintf("Seed.Package%d", i/100)
		v, err := version.ParseNuGet(fmt.Sprintf("1.%d.%d", (i%100)/10, i%10))
		if err != nil {
			return err
		}
		lid, lver := "seed.package"+fmt.Sprint(i/100), v.Normalized()
		p, _ := packagePaths(lid, lver)
		sum := sha256.Sum256([]byte(p))
		rows, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: p, Size: 4096 + int64(i%65536),
			SHA256: hex.EncodeToString(sum[:]), ContentType: assets.FallbackContentType, Kind: assets.KindArtifact}})
		if err != nil {
			return err
		}
		if err := q.InsertVersion(ctx, dbq.InsertVersionParams{RepoID: repo.ID, LowerID: lid, PackageID: id, Version: v.Full(),
			Normalized: v.Normalized(), SortKey: v.SortKey(), Meta: `{"description":"synthetic package"}`, NupkgPath: p,
			AssetID: rows[0].ID, PublishedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
