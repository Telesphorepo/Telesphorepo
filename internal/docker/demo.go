package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// demoRepos is the docker repo of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "docker", Description: "Container images and Helm charts of the demo services.", Public: true}}

// demoImages are the images of the demo repo; the first two are multi-platform indexes and the
// last is a Helm chart.
var demoImages = []string{
	"platform/gateway", "platform/auth", "api", "worker", "scheduler", "web", "billing/invoices",
	"billing/payments", "search", "notifier", "exporter", "migrations", "ledger", "reports",
	"tools/backup", "tools/debug", "edge-proxy", "metrics", "audit-log", "charts/platform",
}

// demo fills the demo repo (module.DemoData): each image gets 1 to 8 tags, each tag a manifest of
// a fresh config and 1 to 3 small layers that the image's tags share in part.
func (f *Format) demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	for i, image := range demoImages {
		tags := 1 + rng.IntN(8)
		offsets := demo.Times(rng, tags)
		base := demoBlob(rng, 2048+rng.IntN(8192))
		for ti := range tags {
			at := run.Anchor - offsets[ti]
			by := run.Uploader(rng, repo, i)
			tag := fmt.Sprintf("1.%d.%d", i%4, ti)
			if ti == tags-1 {
				tag = "latest"
			}
			var ws []assets.Write
			var err error
			switch {
			case i < 2:
				ws, err = demoIndex(repo, image, tag, base, rng, demo.Origin(by, 0, at))
			case i == len(demoImages)-1:
				ws, err = demoManifest(repo, image, tag, ctHelmConfig, "application/vnd.cncf.helm.chart.content.v1.tar+gzip", [][]byte{demoBlob(rng, 3000)}, rng, demo.Origin(by, 0, at), nil)
			default:
				layers := [][]byte{base}
				for range rng.IntN(3) {
					layers = append(layers, demoBlob(rng, 512+rng.IntN(4096)))
				}
				ws, err = demoManifest(repo, image, tag, ctOCIConfig, "application/vnd.oci.image.layer.v1.tar", layers, rng, demo.Origin(by, 0, at), nil)
			}
			if err != nil {
				return err
			}
			run.Expect(len(ws))
			if _, err := run.Assets().CommitAll(ctx, ws); err != nil {
				return fmt.Errorf("demo image %s:%s: %w", image, tag, err)
			}
			run.Stored(len(ws))
		}
	}
	return nil
}

// demoBlob returns n random bytes.
func demoBlob(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return b
}

// demoManifest returns the writes of an image manifest of a fresh config of type configType and
// the layers: the links, the manifest last and, when tag is set, the tag after it. batch holds
// the paths of the commit (an index passes its own); nil starts a new commit.
func demoManifest(repo *repos.Repo, image, tag, configType, layerType string, layers [][]byte, rng *rand.Rand, o assets.Origin, batch map[string]bool) ([]assets.Write, error) {
	if batch == nil {
		batch = map[string]bool{}
	}
	var ws []assets.Write
	link := func(b []byte) Descriptor {
		sp := blobs.Bytes(b)
		p := linkPath(image, sp.SHA256)
		if !batch[p] {
			batch[p] = true
			ws = append(ws, assets.Write{Repo: repo, Path: repopath.Path(p), Body: assets.Body{Spool: sp}, Origin: o,
				Meta: &writeMeta{kind: kindLink, batch: batch}})
		}
		return Descriptor{Digest: "sha256:" + sp.SHA256, Size: int64(len(b))}
	}
	cfg, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "created": time.Unix(o.Updated.Unix(), 0).UTC().Format(time.RFC3339),
		"config": map[string]any{"Labels": map[string]string{"demo.build": demo.Hex(rng, 8)}}})
	if configType == ctHelmConfig {
		cfg, _ = json.Marshal(map[string]any{"name": "platform", "version": tag, "apiVersion": "v2", "description": "Demo chart " + demo.Hex(rng, 6)})
	}
	c := link(cfg)
	c.MediaType = configType
	m := map[string]any{"schemaVersion": 2, "mediaType": mtOCIManifest, "config": c, "layers": []Descriptor{}}
	var ls []Descriptor
	for _, l := range layers {
		d := link(l)
		d.MediaType = layerType
		ls = append(ls, d)
	}
	if ls != nil {
		m["layers"] = ls
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(ws, demoManifestWrites(repo, image, tag, mtOCIManifest, body, o, batch)...), nil
}

// demoManifestWrites returns the write of a manifest and, when tag is set, of its tag.
func demoManifestWrites(repo *repos.Repo, image, tag, mediaType string, body []byte, o assets.Origin, batch map[string]bool) []assets.Write {
	m, _ := ParseManifest(mediaType, body)
	sp := blobs.Bytes(body)
	mp := manifestPath(image, sp.SHA256)
	batch[mp] = true
	ws := []assets.Write{{Repo: repo, Path: repopath.Path(mp), Body: assets.Body{Spool: sp}, ContentType: mediaType, Origin: o,
		Meta: &writeMeta{kind: kindManifest, manifest: m, batch: batch}}}
	if tag != "" {
		tp := tagPath(image, tag)
		batch[tp] = true
		ws = append(ws, assets.Write{Repo: repo, Path: repopath.Path(tp), Body: assets.Body{Blob: sp.SHA256}, ContentType: mediaType, Origin: o,
			Meta: &writeMeta{kind: kindTag, batch: batch}})
	}
	return ws
}

// demoIndex returns the writes of an OCI index of two platform images (linux/amd64 and
// linux/arm64) with its tag.
func demoIndex(repo *repos.Repo, image, tag string, base []byte, rng *rand.Rand, o assets.Origin) ([]assets.Write, error) {
	batch := map[string]bool{}
	var ws []assets.Write
	var children []Descriptor
	for _, arch := range []string{"amd64", "arm64"} {
		mw, err := demoManifest(repo, image, "", ctOCIConfig, "application/vnd.oci.image.layer.v1.tar",
			[][]byte{base, demoBlob(rng, 1024+rng.IntN(2048))}, rng, o, batch)
		if err != nil {
			return nil, err
		}
		ws = append(ws, mw...)
		last := mw[len(mw)-1]
		children = append(children, Descriptor{MediaType: mtOCIManifest, Digest: "sha256:" + last.Body.Spool.SHA256,
			Size: last.Body.Spool.Size, Platform: &Platform{OS: "linux", Architecture: arch}})
	}
	body, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mtOCIIndex, "manifests": children})
	if err != nil {
		return nil, err
	}
	return append(ws, demoManifestWrites(repo, image, tag, mtOCIIndex, body, o, batch)...), nil
}
