package netrules

import (
	"net/netip"
	"slices"
	"sort"
)

// reachIndex finds the repos whose allowed networks hold an address, so a repo list or a search
// costs one lookup, whatever the number of repos with networks. The CIDRs of every repo are cut
// into elementary ranges that each carry the repos covering them; an IP list keeps the repos
// that name it, and its own compiled ranges answer whether it holds the address. A change of the
// networks builds a new index; a refresh of a list changes only the list's ranges.
type reachIndex struct {
	first []netip.Addr // the first address of each elementary range, ascending
	ids   [][]int64    // the repos whose CIDRs cover range i (nil: none)
	lists []listRepos  // the IP lists that allowed networks name, with their repos
}

// listRepos is an IP list with the repos whose allowed networks name it.
type listRepos struct {
	list  int64
	repos []int64
}

// buildReach indexes the allowed networks of rules (by repo id).
func buildReach(rules map[int64]*repoRules) *reachIndex {
	type event struct {
		at    netip.Addr
		repo  int64
		delta int
	}
	var events []event
	byList := map[int64][]int64{}
	for id, rr := range rules {
		for i := range rr.cidrs.first {
			events = append(events, event{rr.cidrs.first[i], id, 1})
			if end := after(rr.cidrs.last[i]); end.IsValid() {
				events = append(events, event{end, id, -1})
			}
		}
		for _, l := range rr.lists {
			byList[l] = append(byList[l], id)
		}
	}
	slices.SortFunc(events, func(a, b event) int { return a.at.Compare(b.at) })
	x := &reachIndex{}
	active := map[int64]int{}
	for i := 0; i < len(events); {
		at := events[i].at
		for ; i < len(events) && events[i].at == at; i++ {
			active[events[i].repo] += events[i].delta
			if active[events[i].repo] == 0 {
				delete(active, events[i].repo)
			}
		}
		var ids []int64
		for id := range active {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		x.first = append(x.first, at)
		x.ids = append(x.ids, ids)
	}
	for l, ids := range byList {
		slices.Sort(ids)
		x.lists = append(x.lists, listRepos{l, slices.Compact(ids)})
	}
	return x
}

// after is the address after the range that ends at last: the next address, or for the last
// IPv4 address the first IPv6 address (netip orders every IPv4 address before every IPv6 one),
// or the zero Addr after the last IPv6 address.
func after(last netip.Addr) netip.Addr {
	if n := last.Next(); n.IsValid() {
		return n
	}
	if last.Is4() {
		return netip.IPv6Unspecified()
	}
	return netip.Addr{}
}

// lookup returns the repos whose networks hold a: the repos of a's elementary range, and the
// repos of each IP list whose ranges (set) hold a.
func (x *reachIndex) lookup(a netip.Addr, set func(list int64) *ipSet) []int64 {
	if x == nil {
		return nil
	}
	var out []int64
	if i := sort.Search(len(x.first), func(i int) bool { return x.first[i].Compare(a) > 0 }) - 1; i >= 0 {
		out = append(out, x.ids[i]...)
	}
	for _, l := range x.lists {
		if set(l.list).contains(a) {
			out = append(out, l.repos...)
		}
	}
	if len(x.lists) > 0 {
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return out
}
