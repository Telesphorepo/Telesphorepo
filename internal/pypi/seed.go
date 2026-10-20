package pypi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/pypi/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// seed inserts n synthetic sdists numbered from start, each with its asset row and its rows
// (module.SeedFunc): 1000 projects with a version per file. A file that exists is kept.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		project := fmt.Sprintf("seedpkg%d", i%1000)
		v, err := version.ParsePEP440(fmt.Sprintf("1.0.%d", i))
		if err != nil {
			return err
		}
		filename := fmt.Sprintf("seedpkg%d-%s.tar.gz", i%1000, v)
		p := "packages/" + project + "/" + filename
		if _, err := q.GetFile(ctx, dbq.GetFileParams{RepoID: repo.ID, Filename: filename}); err == nil {
			continue
		}
		s := sha256.Sum256([]byte(p))
		sum := hex.EncodeToString(s[:])
		size := 2048 + int64(i%65536)
		rows, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: p, Size: size, SHA256: sum, ContentType: "application/gzip", Kind: assets.KindArtifact}})
		if err != nil {
			return err
		}
		if err := q.UpsertProject(ctx, dbq.UpsertProjectParams{RepoID: repo.ID, Project: project, DisplayName: project}); err != nil {
			return err
		}
		if err := q.InsertFile(ctx, dbq.InsertFileParams{RepoID: repo.ID, Project: project, Version: v.String(), SortKey: v.SortKey(),
			Filename: filename, Path: p, AssetID: rows[0].ID, Sha256: sum, Size: size, UploadedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
