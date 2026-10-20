package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The fixed parts of the Worker configuration.
const (
	workerName        = "telesphorepo-edge"
	compatibilityDate = "2026-10-01"
	gapCron           = "*/10 * * * *"
	copyQueueBinding  = "COPY"
)

// versionDefine is the global of the Worker that a build defines to the server version
// (wrangler define): a Worker of another version passes every request to the server.
const versionDefine = "TELESPHOREPO_VERSION"

// versionLiteral is the define value of version: a JavaScript string literal.
func versionLiteral(version string) string {
	b, _ := json.Marshal(version)
	return string(b)
}

// copyQueue is the queue of the blob copies of the home bucket: the R2 event notification rule
// of its blobs/ sends there.
func copyQueue(homeBucket string) string { return homeBucket + "-edge-copy" }

// wrangler returns the wrangler.jsonc of the Worker for the settings of s: the routes (the base
// URL host with the prefix of each format that the edge serves), the bucket bindings (HOME and
// R2_<HINT> of each chosen region), the copy queue (binding COPY), the cron of the gap check, the
// var PREFIX (s3.prefix) and the define of TELESPHOREPO_VERSION to the server version. It fails
// when s cannot run edge delivery.
func (m *mod) wrangler(s *settings.Snapshot) ([]byte, error) {
	if storage.Backend.Get(s) != "s3" {
		return nil, errors.New("edge delivery needs storage s3 (Cloudflare R2)")
	}
	host, err := baseHost(s)
	if err != nil {
		return nil, err
	}
	if HomeHint.Get(s) == "" {
		return nil, errors.New("choose the location of the home bucket (edge.home_hint) first")
	}
	home := storage.S3Bucket.Get(s)
	type r2 struct {
		Binding    string `json:"binding"`
		BucketName string `json:"bucket_name"`
	}
	type producer struct {
		Binding string `json:"binding"`
		Queue   string `json:"queue"`
	}
	type consumer struct {
		Queue           string `json:"queue"`
		MaxBatchSize    int    `json:"max_batch_size"`
		MaxRetries      int    `json:"max_retries"`
		MaxBatchTimeout int    `json:"max_batch_timeout"`
	}
	routes := []string{}
	seen := map[string]bool{}
	for _, f := range m.h.Formats.All() {
		if f.Edge == nil {
			continue
		}
		p := host + f.Edge.Prefix(f.Name) + "*"
		if !seen[p] {
			seen[p] = true
			routes = append(routes, p)
		}
	}
	buckets := []r2{{Binding: "HOME", BucketName: home}}
	for _, rc := range regionsOf(s) {
		buckets = append(buckets, r2{Binding: binding(rc.Hint), BucketName: rc.Bucket})
	}
	cfg := map[string]any{
		"$schema":            "node_modules/wrangler/config-schema.json",
		"name":               workerName,
		"main":               "src/index.ts",
		"compatibility_date": compatibilityDate,
		"workers_dev":        false,
		"routes":             routes,
		"r2_buckets":         buckets,
		"queues": map[string]any{
			"producers": []producer{{Binding: copyQueueBinding, Queue: copyQueue(home)}},
			"consumers": []consumer{{Queue: copyQueue(home), MaxBatchSize: 100, MaxRetries: 10, MaxBatchTimeout: 5}},
		},
		"triggers": map[string]any{"crons": []string{gapCron}},
		"vars":     map[string]string{"PREFIX": storage.S3Prefix.Get(s)},
		// The Worker's VERSION: it serves only while config.json carries the same version.
		"define": map[string]string{versionDefine: versionLiteral(m.h.Version)},
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "// wrangler.jsonc of the Telesphorepo edge Worker, made by the server %s for %s.\n", m.h.Version, host)
	out.WriteString("// Save it in edge/ (or in the folder of the unpacked telesphorepo-edge archive, with \"main\"\n")
	out.WriteString("// set to \"dist/index.js\" and \"no_bundle\": true), then run:\n")
	out.WriteString("//   CLOUDFLARE_API_TOKEN=... bun run provision\n")
	out.WriteString("//   bun run deploy\n")
	out.WriteString("// The token needs Workers Scripts and Routes edit, R2 edit and Queues edit. Run provision again\n")
	out.WriteString("// after a region change, and deploy for each server release.\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}
