package edge

import (
	"encoding/json"
	"strings"
	"time"
)

// gapState is what the server reads of gap/state.json, which the Worker's gap check writes: the
// Worker's build and features (Reported is false when the file names no features), when the
// check last finished a pass, the regions that the Worker has a binding
// for, and the bucket that the bucket.json of each binding names ("" for none). Markers is nil
// when the file has no markers.
type gapState struct {
	Version    string
	Features   []string
	Reported   bool
	CheckedAt  time.Time // the last run of the gap check; zero before the first
	Regions    map[string]bool
	Markers    map[string]string // hint -> bucket
	HomeBucket *string           // the bucket that the HOME binding names; nil when not recorded
	// PassStarted is the start of the copy pass that runs (zero between passes); Copies holds, per
	// region, the copies and deletes that its last finished pass queued.
	PassStarted time.Time
	Copies      map[string]int64
	read        time.Time // when the server read it
}

// parseGap reads gap/state.json: {version, features, checked, last_run, bindings: [hint], markers: {home,
// regions: {hint: bucket}}, pass: {started, ...} | null, last_pass: {started, finished} | null,
// regions: {hint: {last_pass, queued}}}, times in unix seconds. The time of the check is checked
// (every run writes it); a Worker that does not write it gives last_pass.finished, else last_run.
func parseGap(b []byte) *gapState {
	g := &gapState{Regions: map[string]bool{}, Copies: map[string]int64{}}
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		return g
	}
	_ = json.Unmarshal(top["version"], &g.Version)
	g.Reported = json.Unmarshal(top["features"], &g.Features) == nil && g.Features != nil
	var pass map[string]json.RawMessage
	if json.Unmarshal(top["pass"], &pass) == nil && pass != nil {
		g.PassStarted = unixOf(pass["started"])
	}
	var regions map[string]struct {
		Queued int64 `json:"queued"`
	}
	if json.Unmarshal(top["regions"], &regions) == nil {
		for h, r := range regions {
			if h = strings.ToLower(h); isHint(h) {
				g.Copies[h] = r.Queued
			}
		}
	}
	g.CheckedAt = unixOf(top["checked"])
	var last map[string]json.RawMessage
	if g.CheckedAt.IsZero() && json.Unmarshal(top["last_pass"], &last) == nil && last != nil {
		g.CheckedAt = unixOf(last["finished"])
	}
	if g.CheckedAt.IsZero() {
		g.CheckedAt = unixOf(top["last_run"])
	}
	var markers struct {
		Home    *string           `json:"home"`
		Regions map[string]string `json:"regions"`
	}
	if json.Unmarshal(top["markers"], &markers) == nil && (markers.Home != nil || markers.Regions != nil) {
		g.HomeBucket = markers.Home
		g.Markers = map[string]string{}
		for h, b := range markers.Regions {
			if h = strings.ToLower(h); isHint(h) {
				g.Markers[h] = b
			}
		}
	}
	var bindings []string
	_ = json.Unmarshal(top["bindings"], &bindings)
	for _, h := range bindings {
		if h = strings.ToLower(h); isHint(h) {
			g.Regions[h] = true
		}
	}
	return g
}

// unixOf reads a time in unix seconds; zero for none.
func unixOf(raw json.RawMessage) time.Time {
	var n float64
	if json.Unmarshal(raw, &n) != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(n), 0).UTC()
}

// copies returns the copies and deletes that the last finished pass of g queued for hint; ok is
// false before one (or for a nil g).
func (g *gapState) copies(hint string) (int64, bool) {
	if g == nil {
		return 0, false
	}
	q, ok := g.Copies[hint]
	return q, ok
}
