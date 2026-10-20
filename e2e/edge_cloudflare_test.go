//go:build edge

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// TestEdgeCloudflare tests the path that local R2 cannot: an R2 event notification of the home
// bucket reaches the copy queue, and the copy Worker copies a new blob to the regional bucket and
// deletes the copy when the home blob goes. It runs against a real Cloudflare account in the job
// e2e-edge-cloudflare of ci.yml, which provides the account in CLOUDFLARE_ACCOUNT_ID and
// CLOUDFLARE_API_TOKEN (Workers Scripts edit, R2 edit, Queues edit) and an R2 S3 key in
// E2E_R2_ACCESS_KEY_ID and E2E_R2_SECRET_ACCESS_KEY. It makes a home bucket, a regional bucket,
// the queue, the notification rule and the Worker with names of this run, and removes them at
// the end. edge/ needs its dependencies (bun install). Without the account the test skips; with
// E2E_CLOUDFLARE set (the job) it fails.
func TestEdgeCloudflare(t *testing.T) {
	t.Parallel()
	account, token := os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_API_TOKEN")
	keyID, secret := os.Getenv("E2E_R2_ACCESS_KEY_ID"), os.Getenv("E2E_R2_SECRET_ACCESS_KEY")
	if account == "" || token == "" || keyID == "" || secret == "" {
		if os.Getenv("E2E_CLOUDFLARE") != "" {
			t.Fatal("the job needs CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_API_TOKEN, E2E_R2_ACCESS_KEY_ID and E2E_R2_SECRET_ACCESS_KEY")
		}
		t.Skip("no Cloudflare account: this test runs in the e2e-edge-cloudflare job of ci.yml")
	}
	edgeDir, err := filepath.Abs(filepath.Join("..", "edge"))
	if err != nil {
		t.Fatal(err)
	}
	name := "tr-e2e-" + RunID + "-" + randHex(2)
	home, weur := name+"-home", name+"-weur"
	queue := home + "-edge-copy"
	const prefix = "e2e"

	wrangler := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := osexec.CommandContext(ctx, "bun", append([]string{"x", "wrangler"}, args...)...)
		cmd.Dir = edgeDir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := wrangler(args...); err != nil {
			t.Fatalf("wrangler %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	endpoint := "https://" + account + ".r2.cloudflarestorage.com"
	open := func(bucket string) storage.Store {
		t.Helper()
		st, err := storage.NewS3(context.Background(), storage.Config{Backend: "s3", S3Endpoint: endpoint, S3Region: "auto",
			S3Bucket: bucket, S3AccessKeyID: keyID, S3SecretAccessKey: secret, S3Prefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	// Everything this run makes goes at the end, also after a failure half-way.
	t.Cleanup(func() {
		ctx := context.Background()
		steps := [][]string{
			{"delete", "--name", name, "--force"},
			{"r2", "bucket", "notification", "delete", home, "--queue", queue},
			{"queues", "delete", queue},
		}
		for _, s := range steps {
			if out, err := wrangler(s...); err != nil {
				t.Logf("cleanup: wrangler %s: %v\n%s", strings.Join(s, " "), err, out)
			}
		}
		for _, b := range []string{home, weur} {
			st := open(b)
			for {
				objs, err := st.List(ctx, "", "", 1000)
				if err != nil || len(objs) == 0 {
					break
				}
				keys := make([]string, len(objs))
				for i, o := range objs {
					keys[i] = o.Key
				}
				if err := st.DeleteMany(ctx, keys); err != nil {
					t.Logf("cleanup: empty %s: %v", b, err)
					break
				}
			}
			if out, err := wrangler("r2", "bucket", "delete", b); err != nil {
				t.Logf("cleanup: delete the bucket %s: %v\n%s", b, err, out)
			}
		}
	})

	must("r2", "bucket", "create", home)
	must("r2", "bucket", "create", weur, "--location", "weur")
	must("queues", "create", queue)
	must("r2", "bucket", "notification", "create", home, "--event-type", "object-create", "--event-type", "object-delete",
		"--queue", queue, "--prefix", prefix+"/blobs/", "--description", "telesphorepo edge e2e")

	// The Worker reads config.json for the regions to copy to; the server writes it, this test
	// writes the same object.
	homeStore, weurStore := open(home), open(weur)
	config := []byte(fmt.Sprintf(`{"version":"dev","enabled":true,"access_ttl":1,"edge_key":"","formats":{},"home":{"hint":"enam","bucket":%q,"ready":true},`+
		`"regions":[{"hint":"weur","binding":"R2_WEUR","bucket":%q,"ready":true}]}`, home, weur))
	ctx := context.Background()
	if err := homeStore.Put(ctx, "edge/v1/config.json", bytes.NewReader(config), int64(len(config)), "application/json"); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	cfg := fmt.Sprintf(`{
  "name": %q,
  "main": %q,
  "compatibility_date": "2026-10-01",
  "workers_dev": false,
  "r2_buckets": [{"binding": "HOME", "bucket_name": %q}, {"binding": "R2_WEUR", "bucket_name": %q}],
  "queues": {
    "producers": [{"binding": "COPY", "queue": %q}],
    "consumers": [{"queue": %q, "max_batch_size": 100, "max_retries": 10, "max_batch_timeout": 5}]
  },
  "vars": {"PREFIX": %q}
}
`, name, filepath.ToSlash(filepath.Join(edgeDir, "src", "index.ts")), home, weur, queue, queue, prefix)
	cfgFile := filepath.Join(t.TempDir(), "wrangler.jsonc")
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	must("deploy", "--config", cfgFile)

	// A new home blob reaches the region through the notification and the queue.
	body := rawRandom(t, 256<<10)
	key := edgeBlobKey(hexSHA256(body))
	if err := homeStore.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream"); err != nil {
		t.Fatalf("put the home blob: %v", err)
	}
	waitR2(t, "the regional copy of a new blob", func() bool {
		info, err := weurStore.Head(ctx, key)
		return err == nil && info.Size == int64(len(body))
	})

	// The home blob goes: the copy goes too.
	if err := homeStore.Delete(ctx, key); err != nil {
		t.Fatalf("delete the home blob: %v", err)
	}
	waitR2(t, "the delete of the regional copy", func() bool {
		_, err := weurStore.Head(ctx, key)
		return errors.Is(err, storage.ErrNotFound)
	})
}

// waitR2 polls done for up to 5 minutes: notifications and queue batches take seconds to a minute.
func waitR2(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 5 minutes", what)
		}
		time.Sleep(5 * time.Second)
	}
}
