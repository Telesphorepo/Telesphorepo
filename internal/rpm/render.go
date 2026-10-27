package rpm

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// The XML of the index entries follows createrepo_c (src/xml_dump_primary.c,
// xml_dump_filelists.c, xml_dump_other.c, parsehdr.c): the same elements in the same order, the
// same dependency filters.

// esc escapes s for XML text or an attribute value; a character XML cannot hold becomes U+FFFD.
func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// changelogLimit is how many changelog entries the other entry keeps (createrepo_c's default).
const changelogLimit = 10

// dep is one dependency of a package.
type dep struct {
	name, flags, epoch, ver, rel string
	pre                          bool
}

// depTypes are the dependency kinds in the order of the primary entry, with their tags.
var depTypes = []struct {
	elem                 string
	name, flags, version uint32
}{
	{"rpm:provides", tagProvideName, 1112, tagProvideVersion},
	{"rpm:requires", tagRequireName, tagRequireFlags, tagRequireVersion},
	{"rpm:conflicts", tagConflictName, tagConflictFlags, tagConflictVersion},
	{"rpm:obsoletes", tagObsoleteName, tagObsoleteFlags, tagObsoleteVersion},
	{"rpm:suggests", tagSuggestName, tagSuggestFlags, tagSuggestVersion},
	{"rpm:enhances", tagEnhanceName, tagEnhanceFlags, tagEnhanceVersion},
	{"rpm:recommends", tagRecommendName, tagRecommendFlags, tagRecommendVersion},
	{"rpm:supplements", tagSupplementName, tagSupplementFlags, tagSupplementVersion},
}

// flagStr is the flags attribute of a dependency (cr_flag_to_str).
func flagStr(f uint64) string {
	switch f & 0xf {
	case senseLess:
		return "LT"
	case senseGreater:
		return "GT"
	case senseEqual:
		return "EQ"
	case senseLess | senseEqual:
		return "LE"
	case senseGreater | senseEqual:
		return "GE"
	}
	return ""
}

// splitEVR splits a dependency's version [epoch:]version[-release] (cr_str_to_evr); ok is false
// for an epoch that is not a number.
func splitEVR(s string) (epoch, ver, rel string, ok bool) {
	if s == "" {
		return "", "", "", true
	}
	rest := s
	epoch = "0"
	if i := strings.IndexByte(s, ':'); i >= 0 {
		if _, err := strconv.ParseInt(s[:i], 10, 64); err != nil && i > 0 {
			return "", "", "", false
		}
		if i > 0 {
			epoch = s[:i]
		}
		rest = s[i+1:]
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		return epoch, rest[:i], rest[i+1:], true
	}
	return epoch, rest, "", true
}

// deps returns the dependencies of each kind in the order of depTypes, with createrepo_c's
// filters on the requires: no rpmlib(), no primary file the package holds, nothing the package
// provides, no repeat, and only the highest libc.so.6 version, which comes last.
func (p *pkgInfo) deps(files map[string]bool) [][]dep {
	h := p.hdr
	out := make([][]dep, len(depTypes))
	provided := map[string]bool{}
	type seenReq struct {
		flags, version string
		pre            bool
	}
	seen := map[string]seenReq{}
	// The provides are read first, so the requires can drop what the package provides.
	order := []int{0, 2, 3, 1, 4, 5, 6, 7}
	for _, t := range order {
		dt := depTypes[t]
		names, flags, vers := h.strs(dt.name), h.ints(dt.flags), h.strs(dt.version)
		if len(names) == 0 || len(flags) != len(names) || len(vers) != len(names) {
			continue
		}
		var libc *dep
		for i, n := range names {
			fl := flagStr(flags[i])
			full := vers[i]
			nfv := n + fl + full
			pre := false
			if t == 1 {
				if strings.HasPrefix(n, "rpmlib(") {
					continue
				}
				if strings.HasPrefix(n, "/") && files[n] && isPrimaryFile(n) {
					continue
				}
				if provided[nfv] {
					continue
				}
				pre = flags[i]&(sensePrereq|senseScriptPre|sensePosttrans|sensePretrans|senseScriptPost) != 0
				if s, ok := seen[n]; ok && s.flags == fl && s.version == full && s.pre == pre {
					continue
				}
			}
			e, v, r, ok := splitEVR(full)
			if full != "" && !ok {
				continue
			}
			d := dep{name: n, flags: fl, epoch: e, ver: v, rel: r, pre: pre}
			switch t {
			case 0:
				provided[nfv] = true
			case 1:
				if strings.HasPrefix(n, "libc.so.6") {
					if libc == nil || compareLibc(libc.name, n) == 2 {
						libc = &d
					}
					continue
				}
				seen[n] = seenReq{fl, full, pre}
			}
			out[t] = append(out[t], d)
		}
		if libc != nil {
			out[t] = append(out[t], *libc)
		}
	}
	return out
}

// compareLibc compares two libc.so.6 dependencies by the version in their first parentheses
// (cr_compare_dependency): 0 the same, 1 the first is higher, 2 the second is.
func compareLibc(a, b string) int {
	va, ea := libcVersion(a)
	vb, eb := libcVersion(b)
	switch {
	case !ea && !eb:
		return 0
	case !ea:
		return 2
	case !eb:
		return 1
	}
	switch rpmvercmp(va, vb) {
	case 1:
		return 1
	case -1:
		return 2
	}
	return 0
}

// libcVersion returns the version inside the first parentheses of a libc.so.6 dependency, from
// its first digit; false when there is none.
func libcVersion(s string) (string, bool) {
	i := strings.IndexByte(s, '(')
	if i < 0 {
		return "", false
	}
	j := strings.IndexByte(s[i:], ')')
	if j < 0 {
		return "", false
	}
	in := s[i+1 : i+j]
	k := strings.IndexAny(in, "0123456789")
	if k < 0 {
		return "", false
	}
	return in[k:], true
}

// render builds the parts of the package's index entries.
func (p *pkgInfo) render(files []pkgFile) parts {
	h := p.hdr
	var pt parts
	var b strings.Builder
	b.WriteString("<summary>" + esc(h.str(tagSummary)) + "</summary>\n  ")
	b.WriteString("<description>" + esc(h.str(tagDescription)) + "</description>\n  ")
	b.WriteString("<packager>" + esc(h.str(tagPackager)) + "</packager>\n  ")
	b.WriteString("<url>" + esc(h.str(tagURL)) + "</url>")
	pt.Text = b.String()

	b.Reset()
	for _, x := range []struct {
		el  string
		tag uint32
	}{{"license", tagLicense}, {"vendor", tagVendor}, {"group", tagGroup}, {"buildhost", tagBuildHost}, {"sourcerpm", tagSourceRPM}} {
		b.WriteString("<rpm:" + x.el + ">" + esc(h.str(x.tag)) + "</rpm:" + x.el + ">\n    ")
	}
	pt.FormatA = strings.TrimSuffix(b.String(), "\n    ")

	all := make(map[string]bool, len(files))
	for _, f := range files {
		all[f.path] = true
	}
	b.Reset()
	for i, ds := range p.deps(all) {
		if len(ds) == 0 {
			continue
		}
		b.WriteString("\n    <" + depTypes[i].elem + ">")
		for _, d := range ds {
			if d.name == "" {
				continue
			}
			b.WriteString("\n      <rpm:entry name=\"" + esc(d.name) + "\"")
			if d.flags != "" {
				b.WriteString(" flags=\"" + d.flags + "\"")
				if d.epoch != "" {
					b.WriteString(" epoch=\"" + esc(d.epoch) + "\"")
				}
				if d.ver != "" {
					b.WriteString(" ver=\"" + esc(d.ver) + "\"")
				}
				if d.rel != "" {
					b.WriteString(" rel=\"" + esc(d.rel) + "\"")
				}
			}
			if i == 1 && d.pre {
				b.WriteString(" pre=\"1\"")
			}
			b.WriteString("/>")
		}
		b.WriteString("\n    </" + depTypes[i].elem + ">")
	}
	for _, f := range files {
		if isPrimaryFile(f.path) {
			b.WriteString("\n    " + fileElem(f))
		}
	}
	pt.FormatB = b.String()

	b.Reset()
	for _, f := range files {
		b.WriteString("\n    " + fileElem(f))
	}
	pt.Files = b.String()

	pt.Other = p.changelog()
	return pt
}

// fileElem is the <file> element of f.
func fileElem(f pkgFile) string {
	if f.typ != "" {
		return "<file type=\"" + f.typ + "\">" + esc(f.path) + "</file>"
	}
	return "<file>" + esc(f.path) + "</file>"
}

// changelog renders the newest changelogLimit changelog entries, oldest first, with
// createrepo_c's rule for entries of the same time: each earlier one with that time moves a
// second later.
func (p *pkgInfo) changelog() string {
	h := p.hdr
	times, names, texts := h.ints(tagChangelogTime), h.strs(tagChangelogName), h.strs(tagChangelogText)
	n := min(len(times), len(names), len(texts), changelogLimit)
	type entry struct {
		author, text string
		date         int64
	}
	var list []entry // newest first, as createrepo_c prepends
	var last int64
	for i := range n {
		t := int64(times[i])
		list = append([]entry{{strings.TrimRight(names[i], " "), texts[i], t}}, list...)
		if i > 0 && last == t {
			tmp := t
			for j := 1; j < len(list) && list[j].date == tmp; j++ {
				list[j].date++
				tmp++
			}
		} else {
			last = t
		}
	}
	var b strings.Builder
	for _, e := range list {
		b.WriteString("\n    <changelog author=\"" + esc(e.author) + "\" date=\"" + strconv.FormatInt(e.date, 10) + "\">" + esc(e.text) + "</changelog>")
	}
	return b.String()
}
