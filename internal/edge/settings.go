package edge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// Hints are the R2 location hints, in the order the page lists them.
var Hints = []string{"wnam", "enam", "weur", "eeur", "apac", "oc"}

// hintAreas name the area of each hint.
var hintAreas = map[string]string{
	"wnam": "Western North America", "enam": "Eastern North America", "weur": "Western Europe",
	"eeur": "Eastern Europe", "apac": "Asia-Pacific", "oc": "Oceania",
}

func isHint(s string) bool { return slices.Contains(Hints, s) }

// maxAccessTTL is the longest cache time of the access objects at the edge.
const maxAccessTTL = 5 * time.Minute

// bucketRE is an R2 bucket name: lower-case letters, digits and hyphens, 3 to 63 characters, no
// hyphen at either end.
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// The settings of the group "edge".
var (
	Enabled = settings.Bool(&settings.Def{Key: "edge.enabled", Default: "false", Label: "Edge delivery",
		Help: "When this is on, the edge Worker on Cloudflare serves the downloads that it can prove the server would serve the same way. It needs storage s3 on Cloudflare R2 and a base URL without a path."})
	HomeHint = settings.String(&settings.Def{Key: "edge.home_hint", Label: "Location of the home bucket",
		Help:      "The R2 location hint of the S3 bucket of the server: wnam, enam, weur, eeur, apac or oc. The Worker picks the nearest copy, and counts the home bucket at this place. Required when edge delivery is on.",
		Normalize: settings.Lower,
		Validate: func(v any) error {
			if s := v.(string); s != "" && !isHint(s) {
				return fmt.Errorf("must be one of %s", strings.Join(Hints, ", "))
			}
			return nil
		}})
	Regions = settings.List(&settings.Def{Key: "edge.regions", Label: "Regions",
		Help: "The regional buckets, each as <hint>=<bucket>, for example weur=telesphorepo-weur. The server writes the pointers of the files that cannot change to each of them, and the Worker copies the files there.",
		Normalize: func(v any) any {
			l, _ := v.([]string)
			out := make([]string, 0, len(l))
			for _, e := range l {
				h, b, _ := strings.Cut(strings.ToLower(strings.TrimSpace(e)), "=")
				out = append(out, strings.TrimSpace(h)+"="+strings.TrimSpace(b))
			}
			return out
		},
		Validate: func(v any) error {
			for _, e := range v.([]string) {
				h, b, _ := strings.Cut(e, "=")
				if !isHint(h) {
					return fmt.Errorf("%q: the hint must be one of %s", e, strings.Join(Hints, ", "))
				}
				if !bucketRE.MatchString(b) {
					return fmt.Errorf("%q: the bucket must be 3 to 63 lower-case letters, digits and hyphens", e)
				}
			}
			return nil
		}})
	AccessTTL = settings.Duration(&settings.Def{Key: "edge.access_ttl", Default: "30s", Zero: true, Label: "Access cache time",
		Help: "The Worker keeps the access objects and its configuration this long. A revoked token, a removed grant or a disabled user can still download at the edge for this time. 0 reads them on every request: exact, but slower far from the home bucket. At most 5 minutes.",
		Validate: func(v any) error {
			if v.(time.Duration) > maxAccessTTL {
				return errors.New("must be at most 5 minutes")
			}
			return nil
		}})
	CloudflareToken = settings.String(&settings.Def{Key: "edge.cloudflare_token", Secret: true, Label: "Cloudflare API token",
		Normalize: settings.Trim,
		Help:      "An API token of the Cloudflare account of the R2 buckets. With it, the server deploys the edge Worker itself: the regional buckets, the copy queue, its notification rule, the Worker, its cron and its routes. Create it in Manage account › Account API tokens (Create Token, then Custom) with a policy of the Entire Account holding Developer Platform › Workers: Admin, Workers R2 Storage: Edit and Queues: Edit, and a policy of All Domains (or the domain of each route host) holding Developer Platform › Workers Routes: Edit and DNS & Zones › Zone: Read. Add Caching › Cache Purge: Purge to that policy and the Worker caches the pointers of the files that cannot change: the server purges a cached pointer before it replaces or removes its file. Without it the Worker reads every pointer in R2."})
	AutoDeploy = settings.Bool(&settings.Def{Key: "edge.auto_deploy", Default: "true", Label: "Deploy on its own",
		Help: "With a Cloudflare API token, the server deploys the Worker again when the regions, the repository domains, the storage location or the server version change, or when the deployed Worker is outdated."})
)

// defs returns the definitions of the group, in display order.
func defs() []*settings.Def {
	return []*settings.Def{Enabled.Def(), HomeHint.Def(), Regions.Def(), AccessTTL.Def(), CloudflareToken.Def(), AutoDeploy.Def()}
}

// regionCfg is one chosen region: its hint and its bucket.
type regionCfg struct {
	Hint, Bucket string
}

// regionsOf returns the regions of s in the order of Hints, each hint once (the first entry
// wins), without the hint of the home bucket.
func regionsOf(s *settings.Snapshot) []regionCfg {
	by := map[string]string{}
	for _, e := range Regions.Get(s) {
		h, b, _ := strings.Cut(e, "=")
		if _, ok := by[h]; !ok && isHint(h) && b != "" {
			by[h] = b
		}
	}
	var out []regionCfg
	for _, h := range Hints {
		if b, ok := by[h]; ok && h != HomeHint.Get(s) {
			out = append(out, regionCfg{h, b})
		}
	}
	return out
}

// baseHost returns the host of the base URL, or why it does not suit the edge: it must be set
// and have no path, since the Worker routes are the host and the format prefixes.
func baseHost(s *settings.Snapshot) (string, error) {
	raw := settings.BaseURL.Get(s)
	if raw == "" {
		return "", errors.New("the base URL must be set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("the base URL must be an http:// or https:// URL")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("the base URL must have no path")
	}
	return u.Host, nil
}

// unfit returns why the settings of s cannot run edge delivery, or "".
func unfit(s *settings.Snapshot) string {
	if storage.Backend.Get(s) != "s3" {
		return "edge delivery needs storage s3"
	}
	if _, err := baseHost(s); err != nil {
		return "edge delivery needs a base URL without a path: " + err.Error()
	}
	if HomeHint.Get(s) == "" {
		return "edge delivery needs the location of the home bucket"
	}
	return ""
}

// validate checks the group as a whole. At startup a failing rule turns edge delivery off
// (degraded) instead of stopping the server: a storage move to fs or a base URL with a path makes
// it fail.
func (m *mod) validate(d *settings.Draft, mode settings.Mode) error {
	type problem struct{ key, msg string }
	var probs []problem
	next := d.Next
	if Enabled.Get(next) {
		if storage.Backend.Get(next) != "s3" {
			probs = append(probs, problem{Enabled.Name(), "needs storage s3 (Cloudflare R2)"})
		}
		if _, err := baseHost(next); err != nil {
			probs = append(probs, problem{Enabled.Name(), err.Error()})
		}
		if HomeHint.Get(next) == "" {
			probs = append(probs, problem{HomeHint.Name(), "required when edge delivery is on"})
		}
	}
	seen := map[string]bool{}
	home := storage.S3Bucket.Get(next)
	for _, e := range Regions.Get(next) {
		h, b, _ := strings.Cut(e, "=")
		switch {
		case seen[h]:
			probs = append(probs, problem{Regions.Name(), fmt.Sprintf("%s is listed twice", h)})
		case h == HomeHint.Get(next):
			probs = append(probs, problem{Regions.Name(), fmt.Sprintf("%s is the location of the home bucket", h)})
		case b == home:
			probs = append(probs, problem{Regions.Name(), fmt.Sprintf("%s: the bucket %s is the home bucket", h, b)})
		}
		seen[h] = true
	}
	if len(probs) == 0 {
		return nil
	}
	if mode == settings.ModeStartup {
		msgs := make([]string, len(probs))
		for i, p := range probs {
			msgs[i] = p.key + ": " + p.msg
		}
		d.Override(Enabled.Name(), false)
		return fmt.Errorf("%w: edge delivery is off: %s", settings.ErrDegraded, strings.Join(msgs, "; "))
	}
	for _, p := range probs {
		d.Fail(p.key, p.msg)
	}
	return nil
}

// regionConfig is the store configuration of a regional bucket: the storage settings with the
// region's bucket.
func regionConfig(home storage.Config, bucket string) storage.Config {
	c := home
	c.S3Bucket = bucket
	c.S3Redirect = false
	return c
}

// test probes the home bucket and every regional bucket of the draft
// (POST /api/settings/edge/test). A failed regional probe names its row under
// "edge.regions.<hint>", and every failure is also in "edge.regions" or "edge.enabled". With a
// Cloudflare API token and an R2 endpoint, a regional bucket that does not exist passes: the
// deploy from the server creates it.
func (m *mod) test(ctx context.Context, d *settings.Draft) error {
	cfg := storage.ConfigView.Get(d.Next)
	if cfg.Backend != "s3" {
		return &settings.HookError{Status: http.StatusBadRequest, Message: "edge delivery needs storage s3",
			Fields: map[string]string{Enabled.Name(): "needs storage s3 (Cloudflare R2)"}}
	}
	fields := map[string]string{}
	var msgs []string
	if err := probe(ctx, cfg); err != nil {
		msg := "home bucket " + cfg.S3Bucket + ": " + err.Error()
		fields[Enabled.Name()] = msg
		msgs = append(msgs, msg)
	}
	var regionMsgs []string
	account, _, _ := r2Account(cfg.S3Endpoint)
	creates := CloudflareToken.Get(d.Next) != "" && account != ""
	for _, r := range regionsOf(d.Next) {
		if err := probe(ctx, regionConfig(cfg, r.Bucket)); err != nil {
			if creates && noSuchBucket(err) {
				continue // the deploy from the server creates it
			}
			msg := r.Hint + " (" + r.Bucket + "): " + err.Error()
			fields[Regions.Name()+"."+r.Hint] = msg
			regionMsgs = append(regionMsgs, msg)
		}
	}
	if len(regionMsgs) > 0 {
		fields[Regions.Name()] = strings.Join(regionMsgs, "; ")
		msgs = append(msgs, regionMsgs...)
	}
	if len(msgs) > 0 {
		return &settings.HookError{Status: http.StatusBadRequest, Message: "edge test failed: " + strings.Join(msgs, "; "), Fields: fields}
	}
	return nil
}

// noSuchBucket reports whether err is the S3 answer for a bucket that does not exist.
func noSuchBucket(err error) bool {
	var ae interface{ ErrorCode() string }
	return errors.As(err, &ae) && ae.ErrorCode() == "NoSuchBucket"
}

// probe opens the store of cfg and writes, reads and deletes a probe object.
func probe(ctx context.Context, cfg storage.Config) error {
	st, err := storage.Open(ctx, cfg)
	if err != nil {
		return err
	}
	return storage.Probe(ctx, st)
}

// apply runs on every PATCH of the group. After a storage change (deploy needed), saving the
// edge settings says that the Worker of the new home is deployed: the module then starts over in
// the new home. A save while a storage move runs says nothing yet.
func (m *mod) apply(ctx context.Context, c *settings.ApplyCall) (*settings.Applied, error) {
	if !m.moves.Running() {
		m.ack.Store(true)
	}
	m.signal()
	return nil, nil
}
