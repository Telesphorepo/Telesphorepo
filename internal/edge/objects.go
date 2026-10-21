package edge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
)

// root is the prefix of every edge object in a bucket, under s3.prefix (the store adds it).
const root = "edge/v1/"

// The keys of the objects under root.
const (
	configKey     = "config.json"
	markerKey     = "bucket.json"
	gapStateKey   = "gap/state.json"
	gapRequestKey = "gap/request.json"
	checkKey      = "check.json"
	seenPrefix    = "seen/"
	pointerPrefix = "p/"
	accessPrefix  = "a/"
	repoPrefix    = "a/repo/"
	userPrefix    = "a/user/"
	tokenPrefix   = "a/token/"
	devicePrefix  = "a/device/"
)

// pointerKey is the key under root of the pointer of path in repo: the path is hashed, so a
// 1024-byte repo path fits the 1024-byte key limit of R2.
func pointerKey(repoID int64, path string) string {
	sum := sha256.Sum256([]byte(path))
	return pointerPrefix + strconv.FormatInt(repoID, 10) + "/" + hex.EncodeToString(sum[:])
}

// pointer is the object of a stored file.
type pointer struct {
	Repo   int64  `json:"repo"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	SHA1   string `json:"sha1,omitempty"`
	MD5    string `json:"md5,omitempty"`
	Size   int64  `json:"size"`
	CT     string `json:"ct"`
	Mod    int64  `json:"mod"`
	Kind   string `json:"kind"`
}

// pendingPointer is the object of a file while a replacement runs: the Worker passes to the
// server.
type pendingPointer struct {
	Repo    int64  `json:"repo"`
	Path    string `json:"path"`
	Pending bool   `json:"pending"`
}

func pointerOf(a *assets.Asset) []byte {
	b, _ := json.Marshal(pointer{Repo: a.RepoID, Path: a.Path, SHA256: a.SHA256, SHA1: a.SHA1, MD5: a.MD5,
		Size: a.Size, CT: a.ContentType, Mod: a.UpdatedAt.Unix(), Kind: a.Kind})
	return b
}

func pendingOf(repoID int64, path string) []byte {
	b, _ := json.Marshal(pendingPointer{Repo: repoID, Path: path, Pending: true})
	return b
}

// repoObject is a/repo/<name>.
type repoObject struct {
	ID      int64    `json:"id"`
	Format  string   `json:"format"`
	Kind    string   `json:"kind"`
	Anon    bool     `json:"anon"`
	Private bool     `json:"private"`
	Guarded bool     `json:"guarded"`
	Members []string `json:"members"`
}

// userObject is a/user/<id>. All: a server permission that gives every repository; Read: the
// repos where the user holds repo.download.
type userObject struct {
	Enabled bool    `json:"enabled"`
	Local   bool    `json:"local"`
	All     bool    `json:"all"`
	Read    []int64 `json:"read"`
}

// tokenObject is a/token/<public id>. Hash is the hex SHA-256 of the secret; Exp is unix
// seconds, 0 for a token that never expires.
type tokenObject struct {
	ID   int64  `json:"id"`
	User int64  `json:"user"`
	Hash string `json:"hash"`
	Exp  int64  `json:"exp"`
}

// deviceObject is a/device/<id>; Exp is unix seconds.
type deviceObject struct {
	User int64 `json:"user"`
	Exp  int64 `json:"exp"`
}

// configObject is config.json.
type configObject struct {
	Version   string                  `json:"version"`
	Enabled   bool                    `json:"enabled"`
	AccessTTL int64                   `json:"access_ttl"` // seconds
	EdgeKey   string                  `json:"edge_key"`   // base64url (no padding) of the raw Ed25519 public key
	Formats   map[string]formatObject `json:"formats"`
	Home      homeObject              `json:"home"`
	Regions   []regionObject          `json:"regions"`
}

type formatObject struct {
	Shape     string   `json:"shape"`
	Prefix    string   `json:"prefix"`
	Immutable []string `json:"immutable"`
	Mutable   []string `json:"mutable"`
}

// homeObject is the home bucket in config.json. Ready: the outbox wrote every pointer of the
// backfill; before, a missing pointer may be one not written yet, and the Worker answers nothing.
type homeObject struct {
	Hint   string `json:"hint"`
	Bucket string `json:"bucket"`
	Ready  bool   `json:"ready"`
}

// regionObject is a region in config.json. The Worker reads the region only when the bucket.json
// of its binding names Bucket, so a Worker bound to another bucket for the hint does not read it.
type regionObject struct {
	Hint    string `json:"hint"`
	Binding string `json:"binding"`
	Bucket  string `json:"bucket"`
	Ready   bool   `json:"ready"`
}

// markerObject is bucket.json under root of every bucket that the module writes: the bucket
// names itself, so the Worker can check that a binding is the bucket that config.json means.
// Hint is "home" or the region's hint.
type markerObject struct {
	Hint   string `json:"hint"`
	Bucket string `json:"bucket"`
}

// binding is the Worker binding of the bucket of hint.
func binding(hint string) string { return "R2_" + strings.ToUpper(hint) }

// ttlSeconds rounds a cache time up to whole seconds.
func ttlSeconds(d time.Duration) int64 { return int64(math.Ceil(d.Seconds())) }

// formatsObject returns the formats that the Worker serves.
func formatsObject(reg *format.Registry) map[string]formatObject {
	out := map[string]formatObject{}
	for _, s := range reg.All() {
		if s.Edge == nil {
			continue
		}
		out[s.Name] = formatObject{Shape: s.Edge.Shape, Prefix: s.Edge.Prefix(s.Name),
			Immutable: nonNil(s.Edge.Immutable), Mutable: nonNil(s.Edge.Mutable)}
	}
	return out
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func encodeKey(k []byte) string { return base64.RawURLEncoding.EncodeToString(k) }

// hashOf is the hex SHA-256 of an object body, the hash of edge_written.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
