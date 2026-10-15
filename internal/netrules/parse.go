package netrules

import (
	"net/netip"
	"slices"
	"strings"
	"unicode"
)

// Limits of ParseRanges.
const (
	MaxParseText   = 1 << 20 // bytes of pasted text
	maxParseRanges = 100000
	maxInvalid     = 1000 // invalid lines in one answer
	maxInvalidText = 200  // bytes of one invalid line in the answer
)

// InvalidLine is a pasted line that holds no address or range.
type InvalidLine struct {
	Line   int    `json:"line"` // 1-based
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// Parsed is what ParseRanges found in pasted text.
type Parsed struct {
	Ranges     []string      `json:"ranges"`
	Invalid    []InvalidLine `json:"invalid"`
	Duplicates int           `json:"duplicates"`
}

// ParseRanges pulls every IPv4 and IPv6 address and CIDR out of pasted text: one per line, or
// separated by commas, semicolons, spaces, tabs or "|" (CSV and table columns). "#" and "//"
// start a comment. Each token loses surrounding quotes and brackets, trailing punctuation and a
// label such as "runner-1:" or "ip=". An address becomes a /32 or /128, a CIDR is masked to its
// network address, and an IPv4-mapped IPv6 address becomes IPv4. Other tokens are ignored on a
// line that holds a range, and a token without a letter or a digit (a table rule such as "---")
// is no content. A line with content and no range is invalid, except a first line where no token
// starts with a digit (a header row). Duplicates are dropped; the first one keeps its place.
func ParseRanges(text string) (Parsed, error) {
	if len(text) > MaxParseText {
		return Parsed{}, invalid("the text is larger than %d bytes", MaxParseText)
	}
	out := Parsed{Ranges: []string{}, Invalid: []InvalidLine{}}
	seen := map[netip.Prefix]bool{}
	first := true
	for i, line := range strings.Split(text, "\n") {
		line = stripComment(line)
		tokens := slices.DeleteFunc(strings.FieldsFunc(line, isSeparator), func(t string) bool {
			return !strings.ContainsFunc(t, isAlnum)
		})
		if len(tokens) == 0 {
			continue
		}
		header := first
		first = false
		found := false
		for _, tok := range tokens {
			p, ok := parseToken(tok)
			if !ok {
				continue
			}
			found = true
			if seen[p] {
				out.Duplicates++
				continue
			}
			if len(out.Ranges) == maxParseRanges {
				return Parsed{}, invalid("the text holds more than %d ranges", maxParseRanges)
			}
			seen[p] = true
			out.Ranges = append(out.Ranges, p.String())
		}
		if found || (header && isHeader(tokens)) {
			continue
		}
		if len(out.Invalid) < maxInvalid {
			out.Invalid = append(out.Invalid, InvalidLine{Line: i + 1, Text: clipText(strings.TrimSpace(line), maxInvalidText),
				Reason: "not an IP address or range"})
		}
	}
	return out, nil
}

// isHeader reports whether no token starts with a digit, as in "name,ip" or "host | ipv4".
func isHeader(tokens []string) bool {
	for _, t := range tokens {
		t = strings.TrimLeft(t, wrapChars)
		if t != "" && t[0] >= '0' && t[0] <= '9' {
			return false
		}
	}
	return true
}

// stripComment cuts line at the first "#" or "//".
func stripComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	return line
}

func isSeparator(r rune) bool {
	switch r {
	case ',', ';', '|', ' ', '\t', '\r', '\v', '\f', '\u00a0':
		return true
	}
	return false
}

const (
	wrapChars     = "\"'`()[]{}<>"
	trailingPunct = ".,;:!?"
)

// parseToken reads one token as an address or a CIDR. It tries the token as it is, then
// without a label before "=" or the first ":", each also without trailing punctuation.
func parseToken(tok string) (netip.Prefix, bool) {
	tok = strings.Trim(tok, wrapChars)
	cands := []string{tok}
	if i := strings.LastIndexByte(tok, '='); i >= 0 {
		cands = append(cands, strings.Trim(tok[i+1:], wrapChars))
	}
	// A label before ":" must hold a character that is not a hex digit. The first group of an
	// IPv6 address is then never taken for a label.
	if i := strings.IndexByte(tok, ':'); i > 0 && strings.ContainsFunc(tok[:i], notHex) {
		cands = append(cands, strings.Trim(tok[i+1:], wrapChars))
	}
	for _, c := range cands {
		if p, ok := parseOne(c); ok {
			return p, true
		}
		if t := strings.Trim(strings.TrimRight(c, trailingPunct), wrapChars); t != c {
			if p, ok := parseOne(t); ok {
				return p, true
			}
		}
	}
	return netip.Prefix{}, false
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func notHex(r rune) bool {
	return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
}

// parseOne reads an address (a /32 or /128) or a CIDR (masked). An IPv4-mapped IPv6 address or
// range becomes IPv4. Zones are refused.
func parseOne(s string) (netip.Prefix, bool) {
	if s == "" {
		return netip.Prefix{}, false
	}
	if strings.IndexByte(s, '/') >= 0 {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}
