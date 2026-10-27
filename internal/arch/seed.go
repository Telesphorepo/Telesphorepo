package arch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/arch/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// seed inserts n synthetic packages numbered from start, each with its pool file's asset row, its
// file list and its index entries (module.SeedFunc): half for the repo's first architecture,
// half for any. A package that already exists is kept.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	state, err := sha256.New().(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		info := &pkgInfo{Name: fmt.Sprintf("seedpkg%d", i), Version: fmt.Sprintf("1.0.%d-1", i), Arch: architectures(repo)[0], Ext: "zst",
			Fields: map[string][]string{}}
		if i%2 == 1 {
			info.Arch = "any"
		}
		info.Fields["pkgname"] = []string{info.Name}
		info.Fields["pkgver"] = []string{info.Version}
		info.Fields["arch"] = []string{info.Arch}
		info.Fields["pkgdesc"] = []string{fmt.Sprintf("synthetic package %d", i)}
		sum := sha256.Sum256([]byte(info.poolPath()))
		size := 2048 + int64(i%65536)
		info.withFile(size, hex.EncodeToString(sum[:]))
		if _, err := q.GetPackageSHA256(ctx, dbq.GetPackageSHA256Params{RepoID: repo.ID, Name: info.Name, Version: info.Version, Arch: info.Arch}); err == nil {
			continue
		}
		rows, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: info.poolPath(), Size: size, SHA256: hex.EncodeToString(sum[:]),
			ContentType: pkgContentType, Kind: assets.KindArtifact}})
		if err != nil {
			return err
		}
		id, err := q.InsertPackage(ctx, dbq.InsertPackageParams{RepoID: repo.ID, Name: info.Name, Version: info.Version, Arch: info.Arch,
			Vkey: versionKey(info.Version), AssetID: rows[0].ID, Filename: info.poolPath(), Size: size, Sha256: hex.EncodeToString(sum[:]),
			SigState: state, Desc: info.Desc, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := q.InsertPackageFiles(ctx, dbq.InsertPackageFilesParams{PackageID: id,
			Files: "%FILES%\nusr/\nusr/bin/\nusr/bin/" + info.Name + "\n"}); err != nil {
			return err
		}
		for _, t := range targets(repo, info.Arch) {
			if err := q.UpsertEntry(ctx, dbq.UpsertEntryParams{RepoID: repo.ID, Arch: t, Name: info.Name, PackageID: id}); err != nil {
				return err
			}
		}
	}
	return nil
}
