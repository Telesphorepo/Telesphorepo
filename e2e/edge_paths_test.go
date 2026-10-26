//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// getEdgeObject reads the edge object key (under edge/v1/) of bucket; ok=false when it is absent.
func getEdgeObject(tg *S3Target, bucket, key string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	obj, err := tg.Client().GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(tg.Prefix + "/edge/v1/" + key)})
	var gone *s3types.NoSuchKey
	if errors.As(err, &gone) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get %s of %s: %w", key, bucket, err)
	}
	defer obj.Body.Close()
	b, err := io.ReadAll(obj.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read %s of %s: %w", key, bucket, err)
	}
	return b, true, nil
}

func edgeObject(t *testing.T, tg *S3Target, bucket, key string) ([]byte, bool) {
	t.Helper()
	b, ok, err := getEdgeObject(tg, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	return b, ok
}

// edgePointer is a pointer object as the Worker reads it.
type edgePointer struct {
	Repo    int64  `json:"repo"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Pending bool   `json:"pending"`
}

func (p *edgePointer) String() string {
	switch {
	case p == nil:
		return "none"
	case p.Pending:
		return "pending"
	}
	return "sha256 " + p.SHA256[:12]
}

func pointerKeyOf(repoID int64, path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("p/%d/%s", repoID, hex.EncodeToString(sum[:]))
}

// getPointer reads the pointer of path in repo from bucket; nil when there is none.
func getPointer(tg *S3Target, bucket string, repoID int64, path string) (*edgePointer, error) {
	b, ok, err := getEdgeObject(tg, bucket, pointerKeyOf(repoID, path))
	if err != nil || !ok {
		return nil, err
	}
	var p edgePointer
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("pointer of %s: %w: %s", path, err, b)
	}
	return &p, nil
}

func readPointer(t *testing.T, tg *S3Target, bucket string, repoID int64, path string) *edgePointer {
	t.Helper()
	p, err := getPointer(tg, bucket, repoID, path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// edgeRepoID finds the repo id of the pointer of path in the home bucket, waiting for the backfill.
func edgeRepoID(t *testing.T, tg *S3Target, path string) int64 {
	t.Helper()
	ctx := context.Background()
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(300 * time.Millisecond) {
		p := s3.NewListObjectsV2Paginator(tg.Client(), &s3.ListObjectsV2Input{Bucket: &tg.Bucket, Prefix: aws.String(tg.Prefix + "/edge/v1/p/")})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				t.Fatalf("list pointers: %v", err)
			}
			for _, o := range page.Contents {
				b, ok := edgeObject(t, tg, tg.Bucket, strings.TrimPrefix(aws.ToString(o.Key), tg.Prefix+"/edge/v1/"))
				var ptr edgePointer
				if ok && json.Unmarshal(b, &ptr) == nil && ptr.Path == path {
					return ptr.Repo
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pointer of %s in the home bucket after 90 s (keys: %v)", path, tg.ListKeys(t))
		}
	}
}

// waitEdge polls cond until it holds, for at most 90 s.
func waitEdge(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		ok, state := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 90 s; now %s", what, state)
		}
	}
}

// repoPaths reads the paths of the repo object of name in the home bucket.
func repoPaths(t *testing.T, tg *S3Target, name string) [][]any {
	t.Helper()
	b, ok := edgeObject(t, tg, tg.Bucket, "a/repo/"+name)
	if !ok {
		return nil
	}
	var o struct {
		Paths [][]any `json:"paths"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatalf("repo object of %s: %v: %s", name, err, b)
	}
	return o.Paths
}

// TestEdgePathRules runs edge delivery on a SeaweedFS home bucket and one regional bucket without
// a Worker: the server writes every object, and the test reads the buckets. A raw path under deny
// never changes, so its pointer is in every bucket; the repo object carries the rules. A rule
// that makes such a path replaceable leaves no bucket with a pointer of the old bytes that is not
// pending once a replace commits, whatever repo object a Worker holds, and the regional pointer
// goes; a rule that makes a path deny brings its regional pointer.
func TestEdgePathRules(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	region := tg.Bucket + "-enam"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err := tg.Client().CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(region)})
	cancel()
	if err != nil {
		t.Fatalf("create the regional bucket: %v", err)
	}
	s := StartServer(t, ServerOpts{Storage: "s3", S3: tg})

	name, other := RandName("edgeraw"), RandName("edgeallow")
	s.CreateRepo(t, name, "raw", true, map[string]any{"overwrite": "deny"})
	s.CreateRepo(t, other, "raw", true, map[string]any{"overwrite": "allow"})
	pub, pubOther := s.PublishAuth(t, "admin", name), s.PublishAuth(t, "admin", other)
	v1, jar, txt := []byte(`{"build":1}`), []byte("the jar of build 1"), []byte("a file that can change")
	for _, w := range []struct {
		auth Auth
		path string
		body []byte
	}{{pub, "/raw/" + name + "/latest.json", v1}, {pub, "/raw/" + name + "/builds/1/app.jar", jar}, {pubOther, "/raw/" + other + "/notes.txt", txt}} {
		resp, body := rawPut(t, s, w.path, w.body, w.auth)
		rawWantStatus(t, resp, body, http.StatusCreated)
	}
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{
		"edge.home_hint": "weur", "edge.regions": []string{"enam=" + region}, "edge.enabled": true,
	}, s.Admin(), nil)

	sha := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	id, otherID := edgeRepoID(t, tg, "latest.json"), edgeRepoID(t, tg, "notes.txt")
	both := func(repo int64, path string) (home, reg *edgePointer) {
		return readPointer(t, tg, tg.Bucket, repo, path), readPointer(t, tg, region, repo, path)
	}

	// deny: every path of the repo is in the region; allow: home only.
	waitEdge(t, "the backfill writes the pointers", func() (bool, string) {
		h, r := both(id, "latest.json")
		jh, jr := both(id, "builds/1/app.jar")
		oh, or := both(otherID, "notes.txt")
		ok := h != nil && r != nil && h.SHA256 == sha(v1) && r.SHA256 == sha(v1) && jr != nil && jr.SHA256 == sha(jar) &&
			jh != nil && oh != nil && oh.SHA256 == sha(txt) && or == nil
		return ok, fmt.Sprintf("latest.json home %s region %s; app.jar region %s; notes.txt home %s region %s", h, r, jr, oh, or)
	})
	waitEdge(t, "the repo objects carry the rules", func() (bool, string) {
		a, b := repoPaths(t, tg, name), repoPaths(t, tg, other)
		return reflect.DeepEqual(a, [][]any{{"**", true}}) && reflect.DeepEqual(b, [][]any{{"**", false}}), fmt.Sprintf("%v and %v", a, b)
	})

	// latest.json becomes replaceable while the builds stay.
	s.MustAPI(t, http.MethodPatch, "/api/repos/"+name, map[string]any{"config": map[string]any{
		"overwrite": "deny", "rules": []map[string]string{{"match": "latest.json", "overwrite": "allow"}},
	}}, s.Admin(), nil)
	// A Worker may hold the old repo object for edge.access_ttl, and the new one from its write:
	// the replace must leave no pointer of the old bytes that either would serve. Two readers watch
	// both buckets through the replace.
	type seen struct {
		at     time.Time
		bucket string
		p      *edgePointer
		err    error
	}
	var mu sync.Mutex
	var log []seen
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, b := range []string{tg.Bucket, region} {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				at := time.Now()
				p, err := getPointer(tg, b, id, "latest.json")
				mu.Lock()
				log = append(log, seen{at, b, p, err})
				mu.Unlock()
			}
		})
	}
	time.Sleep(300 * time.Millisecond)
	v2 := []byte(`{"build":2}`)
	resp, body := rawPut(t, s, "/raw/"+name+"/latest.json", v2, pub)
	rawWantStatus(t, resp, body, http.StatusOK)
	done := time.Now()
	waitEdge(t, "the replace reaches the buckets", func() (bool, string) {
		h, r := both(id, "latest.json")
		return h != nil && h.SHA256 == sha(v2) && r == nil, fmt.Sprintf("home %s, region %s", h, r)
	})
	close(stop)
	wg.Wait()
	reads := 0
	for _, e := range log {
		if e.err != nil {
			t.Fatal(e.err)
		}
		if e.at.After(done) {
			reads++
			if e.p != nil && !e.p.Pending && e.p.SHA256 == sha(v1) {
				t.Fatalf("%s holds the pointer of the old bytes %v after the replace committed", e.bucket, e.at.Sub(done))
			}
		}
	}
	if reads == 0 {
		t.Fatalf("no read of the buckets after the replace")
	}
	if _, r := both(id, "builds/1/app.jar"); r == nil || r.SHA256 != sha(jar) {
		t.Fatalf("the regional pointer of builds/1/app.jar is %s, want the jar: deny paths stay in the region", r)
	}
	waitEdge(t, "the repo object carries the new rules", func() (bool, string) {
		p := repoPaths(t, tg, name)
		return reflect.DeepEqual(p, [][]any{{"latest.json", false}, {"**", true}}), fmt.Sprint(p)
	})

	// A path that becomes deny gets its regional pointer from the walk.
	s.MustAPI(t, http.MethodPatch, "/api/repos/"+other, map[string]any{"config": map[string]any{"overwrite": "deny"}}, s.Admin(), nil)
	waitEdge(t, "the walk writes the regional pointer of notes.txt", func() (bool, string) {
		_, r := both(otherID, "notes.txt")
		return r != nil && r.SHA256 == sha(txt), "region " + r.String()
	})

	// While edge delivery drains, the pre-steps run and nothing else writes: a replace of a path
	// that the rules no longer keep makes the pointer pending in every bucket, the region too,
	// since a Worker with the old repo object still reads it there.
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.enabled": false}, s.Admin(), nil)
	waitEdge(t, "the drain writes the off switch", func() (bool, string) {
		b, _ := edgeObject(t, tg, tg.Bucket, "config.json")
		var c struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.Unmarshal(b, &c)
		return !c.Enabled, string(b)
	})
	s.MustAPI(t, http.MethodPatch, "/api/repos/"+name, map[string]any{"config": map[string]any{"overwrite": "allow"}}, s.Admin(), nil)
	resp, body = rawPut(t, s, "/raw/"+name+"/builds/1/app.jar", []byte("the jar, rebuilt"), pub)
	rawWantStatus(t, resp, body, http.StatusOK)
	if h, r := both(id, "builds/1/app.jar"); h == nil || !h.Pending || r == nil || !r.Pending {
		t.Fatalf("after a replace during the drain: home %s, region %s; want pending in both", h, r)
	}
}
