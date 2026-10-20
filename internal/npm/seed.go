package npm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// seed inserts n synthetic versions numbered from start, 100 per package, each with its tarball
// asset row (module.SeedFunc). A version that exists is kept.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	now := time.Now()
	touched := map[string]bool{}
	for i := start; i < start+n; i++ {
		name, v := fmt.Sprintf("seed-pkg-%d", i/100), fmt.Sprintf("1.0.%d", i%100)
		p := tarballPath(name, v)
		if _, err := q.GetVersionTarball(ctx, dbq.GetVersionTarballParams{RepoID: repo.ID, Name: name, Version: v}); err == nil {
			continue
		}
		sum := sha256.Sum256([]byte(p))
		if _, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: p, Size: 4096 + int64(i%4096),
			SHA256: hex.EncodeToString(sum[:]), ContentType: "application/octet-stream", Kind: assets.KindArtifact}}); err != nil {
			return err
		}
		sv, _ := version.ParseSemVer(v)
		manifest := fmt.Sprintf(`{"name":%q,"version":%q,"_id":"%s@%s"}`, name, v, name, v)
		if err := q.InsertVersion(ctx, dbq.InsertVersionParams{RepoID: repo.ID, Name: name, Version: v, SortKey: sv.SortKey(),
			Manifest: manifest, Tarball: p, Integrity: "sha512-" + hex.EncodeToString(sum[:]), Shasum: hex.EncodeToString(sum[:20]),
			PublishedAt: now.Unix()}); err != nil {
			return err
		}
		touched[name] = true
	}
	for name := range touched {
		if err := refresh(ctx, q, repo.ID, name, now); err != nil {
			return err
		}
	}
	return nil
}
