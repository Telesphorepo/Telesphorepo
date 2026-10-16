package docker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// seed inserts n synthetic images numbered from start (module.SeedFunc): each is a tag, a manifest
// and one layer with their asset rows and docker rows, in images of 100 tags each. A number that
// exists already is kept.
func (f *Format) seed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, start, n int) error {
	q := dbq.New(tx)
	now := time.Now().Unix()
	sum := func(s string) string {
		b := sha256.Sum256([]byte(s))
		return hex.EncodeToString(b[:])
	}
	for i := start; i < start+n; i++ {
		image := fmt.Sprintf("seed/img%d", i/100)
		tag := fmt.Sprintf("v%d", i)
		layer, manifest := sum(fmt.Sprintf("seed layer %d", i)), sum(fmt.Sprintf("seed manifest %d", i))
		size := 4096 + int64(i%65536)
		if _, err := assets.SeedTx(ctx, tx, repo.ID, []assets.SeedRow{
			{Path: linkPath(image, layer), Size: size, SHA256: layer, ContentType: assets.FallbackContentType, Kind: assets.KindIndex},
			{Path: manifestPath(image, manifest), Size: 400, SHA256: manifest, ContentType: mtOCIManifest, Kind: assets.KindMetadata},
			{Path: tagPath(image, tag), Size: 400, SHA256: manifest, ContentType: mtOCIManifest, Kind: assets.KindArtifact},
		}); err != nil {
			return err
		}
		if _, err := q.GetManifest(ctx, dbq.GetManifestParams{RepoID: repo.ID, Image: image, Digest: manifest}); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := q.UpsertLink(ctx, dbq.UpsertLinkParams{RepoID: repo.ID, Image: image, Digest: layer, Size: size, Refs: 1}); err != nil {
			return err
		}
		if err := q.InsertManifest(ctx, dbq.InsertManifestParams{RepoID: repo.ID, Image: image, Digest: manifest, MediaType: mtOCIManifest,
			ConfigType: ctOCIConfig, ArtifactType: ctOCIConfig, Size: 400, TotalSize: 400 + size, Platforms: "[]", Annotations: "{}", CreatedAt: now}); err != nil {
			return err
		}
		if err := q.InsertRef(ctx, dbq.InsertRefParams{RepoID: repo.ID, Image: image, Manifest: manifest, Child: layer, Kind: "blob"}); err != nil {
			return err
		}
		if err := q.UpsertImage(ctx, dbq.UpsertImageParams{RepoID: repo.ID, Image: image, FullName: repo.Name + "/" + image}); err != nil {
			return err
		}
		if err := q.AddImageManifests(ctx, dbq.AddImageManifestsParams{Delta: 1, RepoID: repo.ID, Image: image}); err != nil {
			return err
		}
		if err := q.UpsertTag(ctx, dbq.UpsertTagParams{RepoID: repo.ID, Image: image, Tag: tag, Digest: manifest, UpdatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
