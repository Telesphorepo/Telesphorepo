package rpm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
)

// seed inserts n synthetic packages numbered from start, each with its file's asset row and the
// parts of its index entries (module.SeedFunc): 100 versions of each name, one file in /usr/bin
// each, half x86_64, half noarch. A package that already exists is kept.
func seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	state, err := sha256.New().(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for i := start; i < start+n; i++ {
		info := &pkgInfo{Name: fmt.Sprintf("seedpkg%d", i/100), Version: fmt.Sprintf("1.0.%d", i%100), Release: "1", Arch: "x86_64", Epoch: -1,
			Summary: fmt.Sprintf("synthetic package %d", i)}
		if i%2 == 1 {
			info.Arch = "noarch"
		}
		if _, err := q.GetSums(ctx, identity(repo.ID, info)); err == nil {
			continue
		}
		bin := "/usr/bin/" + info.Name
		pt := parts{Text: "<summary>" + info.Summary + "</summary>\n  <description>" + info.Summary + "</description>\n  <packager></packager>\n  <url></url>",
			FormatA: "<rpm:license>MIT</rpm:license>\n    <rpm:vendor></rpm:vendor>\n    <rpm:group>Unspecified</rpm:group>\n    <rpm:buildhost>seed</rpm:buildhost>\n    <rpm:sourcerpm></rpm:sourcerpm>",
			FormatB: "\n    <rpm:provides>\n      <rpm:entry name=\"" + info.Name + "\" flags=\"EQ\" epoch=\"0\" ver=\"" + info.Version + "\" rel=\"1\"/>\n    </rpm:provides>\n    " + fileElem(pkgFile{path: bin}),
			Files:   "\n    " + fileElem(pkgFile{path: bin})}
		sum := sha256.Sum256([]byte(info.path()))
		size := 2048 + int64(i%65536)
		rows, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{{Path: info.path(), Size: size, SHA256: hex.EncodeToString(sum[:]),
			ContentType: rpmContentType, Kind: assets.KindArtifact}})
		if err != nil {
			return err
		}
		id, err := q.InsertPackage(ctx, dbq.InsertPackageParams{RepoID: repo.ID, Name: info.Name, Epoch: info.Epoch, Version: info.Version,
			Release: info.Release, Arch: info.Arch, Evr: info.evr(), Vkey: versionKey(info.Epoch, info.Version, info.Release),
			AssetID: rows[0].ID, Filename: info.path(), Size: size, Sha256: hex.EncodeToString(sum[:]), UploadSha256: hex.EncodeToString(sum[:]),
			Format: 4, HdrState: state, HeaderStart: 1024, HeaderLen: 2048, BuildTime: now, InstalledSize: 1024, ArchiveSize: 1024,
			Summary: info.Summary, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := q.InsertParts(ctx, dbq.InsertPartsParams{PackageID: id, Text: pt.Text, FormatA: pt.FormatA, FormatB: pt.FormatB,
			Files: pt.Files, Other: pt.Other}); err != nil {
			return err
		}
	}
	return nil
}
