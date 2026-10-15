package nexus

import (
	"fmt"
	"strconv"
	"time"
)

// count writes n with thousands separators: 12400 -> "12,400".
func count(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// files writes n with the noun: "1 new file", "34 new files".
func files(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return count(n) + " " + noun + "s"
}

var sizeUnits = []string{"B", "KiB", "MiB", "GiB", "TiB"}

// size writes a byte count as the UI does: 1536 -> "1.5 KiB".
func size(n int64) string {
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(sizeUnits)-1 {
		v /= 1024
		i++
	}
	switch {
	case i == 0:
		return strconv.FormatInt(n, 10) + " B"
	case v < 10:
		return fmt.Sprintf("%.1f %s", v, sizeUnits[i])
	}
	return fmt.Sprintf("%.0f %s", v, sizeUnits[i])
}

// duration writes d in words: "40 seconds", "6 minutes", "2 hours 5 minutes".
func duration(d time.Duration) string {
	unit := func(n int64, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return strconv.FormatInt(n, 10) + " " + word + "s"
	}
	switch {
	case d < time.Minute:
		return unit(int64(d/time.Second), "second")
	case d < time.Hour:
		return unit(int64(d/time.Minute), "minute")
	}
	h, m := int64(d/time.Hour), int64(d%time.Hour/time.Minute)
	if m == 0 {
		return unit(h, "hour")
	}
	return unit(h, "hour") + " " + unit(m, "minute")
}
