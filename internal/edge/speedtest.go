package edge

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The speed test of the Edge delivery page. POST /api/edge/speedtest writes a pass object to the
// home bucket, speedtest/<hex SHA-256 of a random 256-bit token>.json = {hash, exp}, and answers
// the token. A download with Telesphorepo-Edge-Region: <hint>|home|origin and
// Telesphorepo-Edge-Pass: <token> then makes the Worker read from that bucket, or pass to the
// server, after its usual access checks (see edge/src/speedtest.ts). GET /api/edge/speedtest
// suggests files to download: immutable files of public repos that the Worker serves.
const (
	speedPrefix = "speedtest/"
	// speedTTL is how long a pass works.
	speedTTL = 10 * time.Minute
	// speedRepos and speedScan bound the suggestion: the first speedRepos public repos, the first
	// speedScan files of each in path order.
	speedRepos = 16
	speedScan  = 500
	speedFiles = 8
)

// speedPassJSON is the answer of POST /api/edge/speedtest.
type speedPassJSON struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// speedFileJSON is one suggested file: URL is its download path on the base URL host.
type speedFileJSON struct {
	Repo   string `json:"repo"`
	Format string `json:"format"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

// speedFilesJSON is the answer of GET /api/edge/speedtest. BaseURL is the scheme and host of the
// base URL ("" when it is not set); Unfit says why a pass cannot be issued ("" when it can).
type speedFilesJSON struct {
	BaseURL string          `json:"base_url"`
	Unfit   string          `json:"unfit"`
	Files   []speedFileJSON `json:"files"`
}

// speedPass issues a pass of the speed test.
func (m *mod) speedPass(w http.ResponseWriter, r *http.Request, c *route.Call) {
	if _, why := checkBase(m.h.Settings.Current()); why != "" {
		ProblemNotConfigured.Write(w, r, why)
		return
	}
	home, err := m.gapHome(r.Context())
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "The home bucket did not open: "+errText(err))
		return
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	exp := time.Now().Add(speedTTL).Truncate(time.Second)
	body, _ := json.Marshal(map[string]any{"hash": hash, "exp": exp.Unix()})
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	err = home.st.Put(ctx, root+speedPrefix+hash+".json", bytes.NewReader(body), int64(len(body)), "application/json")
	cancel()
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, "Writing the pass to the home bucket failed: "+errText(err))
		return
	}
	m.h.Go("edge.speedtest.sweep", func(ctx context.Context) { m.sweepPasses(ctx, home) })
	httpx.WriteJSON(w, http.StatusOK, speedPassJSON{Token: token, ExpiresAt: exp.UTC()})
}

// sweepPasses deletes the pass objects that expired: each was written speedTTL before it expires.
func (m *mod) sweepPasses(ctx context.Context, home *bucket) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	objs, err := home.st.List(ctx, root+speedPrefix, "", 1000)
	if err != nil {
		return
	}
	var old []string
	for _, o := range objs {
		if !o.ModTime.IsZero() && time.Since(o.ModTime) > speedTTL+time.Minute {
			old = append(old, o.Key)
		}
	}
	if len(old) > 0 {
		_ = home.st.DeleteMany(ctx, old)
	}
}

// speedFiles suggests the files of the speed test: the largest immutable files among the first
// files of the public repos whose format the Worker serves, at most two of each repo.
func (m *mod) speedFiles(w http.ResponseWriter, r *http.Request, c *route.Call) {
	snap := m.h.Settings.Current()
	out := speedFilesJSON{Files: []speedFileJSON{}}
	_, out.Unfit = checkBase(snap)
	if u, err := url.Parse(settings.BaseURL.Get(snap)); err == nil && u.Host != "" {
		out.BaseURL = u.Scheme + "://" + u.Host
	}
	all, err := m.h.Repos.List(r.Context())
	if err != nil {
		httpx.Error(w, r, http.StatusInternalServerError, "the repositories did not load")
		return
	}
	scanned := 0
	for _, repo := range all {
		e := m.edgeOf(repo)
		if e == nil || !repo.AnonymousRead || repo.Kind == repos.KindGroup || m.h.Access.Guarded(repo) {
			continue
		}
		if scanned++; scanned > speedRepos {
			break
		}
		list, _, err := m.h.Assets.List(r.Context(), repo.ID, "", "", speedScan)
		if err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "the files did not load")
			return
		}
		var picks []speedFileJSON
		for _, a := range list {
			if !e.IsImmutable(a.Path) {
				continue
			}
			if u := downloadURL(e, repo, a.Path); u != "" {
				picks = append(picks, speedFileJSON{Repo: repo.Name, Format: repo.Format, Path: a.Path, Size: a.Size, URL: u})
			}
		}
		slices.SortFunc(picks, func(a, b speedFileJSON) int { return cmp.Compare(b.Size, a.Size) })
		out.Files = append(out.Files, picks[:min(2, len(picks))]...)
	}
	slices.SortFunc(out.Files, func(a, b speedFileJSON) int { return cmp.Compare(b.Size, a.Size) })
	out.Files = out.Files[:min(speedFiles, len(out.Files))]
	httpx.WriteJSON(w, http.StatusOK, out)
}

// downloadURL is the path of the URL that downloads path of repo, in the URL shape of the format:
// /<format>/<repo>/<path>, or on the oci shape the blob or the manifest by digest. "" when the
// path has no such URL.
func downloadURL(e *format.EdgeSpec, repo *repos.Repo, p string) string {
	if e.Shape != format.EdgeOCI {
		segs := strings.Split(p, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		return e.Prefix(repo.Format) + url.PathEscape(repo.Name) + "/" + strings.Join(segs, "/")
	}
	for _, k := range []struct{ marker, action string }{{"/_blobs/sha256/", "blobs"}, {"/_manifests/sha256/", "manifests"}} {
		if image, hexDigest, ok := strings.Cut(p, k.marker); ok && image != "" && hexDigest != "" && !strings.Contains(hexDigest, "/") {
			return e.Prefix(repo.Format) + repo.Name + "/" + image + "/" + k.action + "/sha256:" + hexDigest
		}
	}
	return ""
}
