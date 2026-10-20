package edge

import (
	"encoding/json"
	"strings"
	"time"
)

// gapState is what the server reads of gap/state.json, which the Worker's gap check writes: the
// Worker version, when the check last finished a pass, the regions that the Worker has a binding
// for, and the bucket that the bucket.json of each binding names ("" for none). Markers is nil
// when the file has no markers.
type gapState struct {
	Version    string
	CheckedAt  time.Time // zero before the first pass
	Regions    map[string]bool
	Markers    map[string]string // hint -> bucket
	HomeBucket *string           // the bucket that the HOME binding names; nil when not recorded
	read       time.Time         // when the server read it
}

// parseGap reads gap/state.json: {version, last_run, bindings: [hint], markers: {home, regions:
// {hint: bucket}}, last_pass: {started, finished} | null, regions: {hint: {last_pass, queued}},
// ...}, times in unix seconds. The time of the check is last_pass.finished, else last_run.
func parseGap(b []byte) *gapState {
	g := &gapState{Regions: map[string]bool{}}
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		return g
	}
	_ = json.Unmarshal(top["version"], &g.Version)
	var last map[string]json.RawMessage
	if json.Unmarshal(top["last_pass"], &last) == nil && last != nil {
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
