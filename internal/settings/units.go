package settings

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const sizeHint = `must be a size such as "512MiB", "2 GiB", "1.5GiB" or "500MB" (a bare number is bytes)`

var sizeRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([a-z]*)$`)

// sizeUnits maps a lower-case unit to its size in bytes: binary (KiB) and decimal (KB).
var sizeUnits = map[string]float64{
	"": 1, "b": 1,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
}

// parseSize reads a number with an optional unit ("512MiB", "1.5 GiB", "500mb", "4096") and
// returns the size in bytes.
func parseSize(s string) (int64, error) {
	m := sizeRE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, errors.New(sizeHint)
	}
	mult, ok := sizeUnits[m[2]]
	if !ok {
		return 0, fmt.Errorf("unknown unit %q; use B, KiB, MiB, GiB, TiB, KB, MB, GB or TB", m[2])
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, errors.New(sizeHint)
	}
	b := f * mult
	if b >= 1<<53 {
		return 0, errors.New("is too large")
	}
	if math.Abs(b-math.Round(b)) > 1e-6 {
		return 0, errors.New("must be a whole number of bytes")
	}
	return int64(math.Round(b)), nil
}

var binaryUnits = []struct {
	name string
	size int64
}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}}

// formatSize shows bytes in the largest binary unit that gives a whole number ("2 GiB",
// "1536 MiB"); a size that no unit divides shows with one decimal ("476.8 MiB").
func formatSize(n int64) string {
	for _, u := range binaryUnits {
		if n >= u.size && n%u.size == 0 {
			return fmt.Sprintf("%d %s", n/u.size, u.name)
		}
	}
	for _, u := range binaryUnits {
		if n >= u.size {
			return fmt.Sprintf("%.1f %s", float64(n)/float64(u.size), u.name)
		}
	}
	return fmt.Sprintf("%d B", n)
}

var durationRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([a-zµμ]+)[\s,]*`)

var durationUnits = map[string]time.Duration{
	"ns": time.Nanosecond, "us": time.Microsecond, "µs": time.Microsecond, "μs": time.Microsecond,
	"ms": time.Millisecond,
	"s":  time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
}

// parseDuration reads Go durations ("1h30m") and friendly forms ("15 minutes", "90 days",
// "365d", "1 hour 30 minutes").
func parseDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var total float64
	for s != "" {
		m := durationRE.FindStringSubmatch(s)
		if m == nil {
			return 0, errors.New("invalid duration")
		}
		unit, ok := durationUnits[m[2]]
		if !ok {
			return 0, fmt.Errorf("unknown unit %q", m[2])
		}
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, err
		}
		total += f * float64(unit)
		s = s[len(m[0]):]
	}
	if total >= math.MaxInt64 {
		return 0, errors.New("duration too large")
	}
	return time.Duration(total), nil
}

// canonicalDuration is the stored text of a duration: Go syntax without zero parts ("15m",
// "2160h", "1h30m").
func canonicalDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// formatDuration shows a duration in words ("15 minutes", "365 days", "1 hour 30 minutes").
func formatDuration(d time.Duration) string {
	units := []struct {
		name string
		size time.Duration
	}{{"day", 24 * time.Hour}, {"hour", time.Hour}, {"minute", time.Minute}, {"second", time.Second}, {"millisecond", time.Millisecond}}
	var parts []string
	for _, u := range units {
		if n := d / u.size; n > 0 {
			name := u.name
			if n != 1 {
				name += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
			d -= n * u.size
		}
	}
	if len(parts) == 0 {
		return d.String()
	}
	return strings.Join(parts, " ")
}
