package netrules

import (
	"net/netip"
	"slices"
	"sort"
)

// ipSet is a set of prefixes compiled for lookup: the address ranges they cover, sorted by first
// address and merged where they overlap or touch, so a lookup is a binary search.
type ipSet struct {
	first, last []netip.Addr
}

// newIPSet compiles ps.
func newIPSet(ps []netip.Prefix) *ipSet {
	type span struct{ first, last netip.Addr }
	spans := make([]span, 0, len(ps))
	for _, p := range ps {
		if p.IsValid() {
			spans = append(spans, span{p.Masked().Addr(), lastAddr(p)})
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return a.first.Compare(b.first) })
	s := &ipSet{}
	for _, sp := range spans {
		if n := len(s.last); n > 0 && s.first[n-1].BitLen() == sp.first.BitLen() {
			end := s.last[n-1]
			if sp.first.Compare(end) <= 0 || (end.Next().IsValid() && sp.first == end.Next()) {
				if sp.last.Compare(end) > 0 {
					s.last[n-1] = sp.last
				}
				continue
			}
		}
		s.first = append(s.first, sp.first)
		s.last = append(s.last, sp.last)
	}
	return s
}

// lastAddr returns the last address of p.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// contains reports whether a is in s, as netip.Prefix.Contains would for one of its prefixes: an
// IPv4 address never matches an IPv6 range, and an invalid or zoned address matches nothing.
func (s *ipSet) contains(a netip.Addr) bool {
	if s == nil || !a.IsValid() || a.Zone() != "" {
		return false
	}
	i := sort.Search(len(s.first), func(i int) bool { return s.first[i].Compare(a) > 0 }) - 1
	return i >= 0 && s.last[i].Compare(a) >= 0
}
