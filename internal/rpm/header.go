package rpm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"
)

// An RPM file is a 96-byte lead, the signature header (padded to 8 bytes), the header and the
// compressed payload. Each header is the magic 8e ad e8 01, four reserved bytes, the index count
// il and the data size dl (big-endian), il index entries of 16 bytes (tag, type, offset, count)
// and dl bytes of data. Its first entry is the region tag (62 in the signature header, 63 in the
// header) whose data, the 16-byte trailer at the end of the data, closes the region. The layout
// follows rpm's lib/header.cc (hdrblobVerifyInfo, hdrblobVerifyRegion, doExport).

const (
	leadSize       = 96
	headerIntro    = 16 // magic, reserved, il, dl
	entrySize      = 16
	regionSize     = 16 // the region trailer, REGION_TAG_COUNT
	maxIndexCount  = 0xffff
	maxSigIndex    = 32 // rpm reads at most 32 entries of a signature header
	maxSigDataSize = 64 << 20
)

var (
	leadMagic   = []byte{0xed, 0xab, 0xee, 0xdb}
	headerMagic = []byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0}
)

// The header data types (rpmTagType).
const (
	typeNull        = 0
	typeChar        = 1
	typeInt8        = 2
	typeInt16       = 3
	typeInt32       = 4
	typeInt64       = 5
	typeString      = 6
	typeBin         = 7
	typeStringArray = 8
	typeI18NString  = 9
)

// typeAlign is the alignment of each type in the data, rpm's typeAlign.
var typeAlign = [10]int{1, 1, 1, 2, 4, 8, 1, 1, 1, 1}

// typeSize is the size of one item of a fixed-size type; 0 for a string type.
var typeSize = [10]int{0, 1, 1, 2, 4, 8, 0, 1, 0, 0}

// The region tags.
const (
	tagHeaderSignatures = 62
	tagHeaderImmutable  = 63
)

// entry is one tag of a header with its data in the file's byte order (big-endian).
type entry struct {
	Tag, Type, Count uint32
	Data             []byte
}

// header is a parsed header: its entries without the region tag, in tag order.
type header struct {
	entries []entry
}

// errBadHeader is a header that does not parse; the message says why.
type errBadHeader string

func (e errBadHeader) Error() string { return "invalid RPM header: " + string(e) }

// readHeader reads one header from r: the intro, the index and the data, at most maxData bytes of
// data. It returns the header and its bytes from the magic on (what the header's digests and
// signatures cover).
func readHeader(r io.Reader, maxIndex uint32, maxData int) (*header, []byte, error) {
	intro := make([]byte, headerIntro)
	if _, err := io.ReadFull(r, intro); err != nil {
		return nil, nil, errBadHeader("the file ends inside a header")
	}
	if !bytes.Equal(intro[:3], headerMagic[:3]) || intro[3] != 1 {
		return nil, nil, errBadHeader("bad header magic")
	}
	il := binary.BigEndian.Uint32(intro[8:])
	dl := binary.BigEndian.Uint32(intro[12:])
	if il < 1 || il > maxIndex {
		return nil, nil, errBadHeader(fmt.Sprintf("index count %d", il))
	}
	if int64(dl) > int64(maxData) {
		return nil, nil, errBadHeader(fmt.Sprintf("the header holds %d bytes of data, more than the limit of %d", dl, maxData))
	}
	// The buffer grows as the bytes arrive, so a header that claims more than the file holds
	// costs only what the file holds.
	var buf bytes.Buffer
	buf.Write(intro)
	want := int64(il)*entrySize + int64(dl)
	if n, _ := io.CopyN(&buf, r, want); n != want {
		return nil, nil, errBadHeader("the file ends inside a header")
	}
	raw := buf.Bytes()
	h, err := parseHeader(raw)
	if err != nil {
		return nil, nil, err
	}
	return h, raw, nil
}

// parseHeader parses the bytes of a header from its magic on, checking each entry as rpm does:
// a known type, data in range and aligned, no entry overlapping the one before.
func parseHeader(raw []byte) (*header, error) {
	il := binary.BigEndian.Uint32(raw[8:])
	dl := binary.BigEndian.Uint32(raw[12:])
	index := raw[headerIntro : headerIntro+int(il)*entrySize]
	data := raw[headerIntro+int(il)*entrySize:]
	if uint32(len(data)) != dl {
		return nil, errBadHeader("data size")
	}
	h := &header{}
	end := uint32(0)
	for i := uint32(0); i < il; i++ {
		e := index[i*entrySize:]
		tag := binary.BigEndian.Uint32(e)
		typ := binary.BigEndian.Uint32(e[4:])
		off := binary.BigEndian.Uint32(e[8:])
		count := binary.BigEndian.Uint32(e[12:])
		if i == 0 && (tag == tagHeaderSignatures || tag == tagHeaderImmutable) {
			if typ != typeBin || count != regionSize || int64(off)+regionSize > int64(dl) {
				return nil, errBadHeader("region tag")
			}
			continue
		}
		if typ > typeI18NString || typ == typeNull {
			return nil, errBadHeader(fmt.Sprintf("tag %d has type %d", tag, typ))
		}
		if count == 0 || count > dl {
			return nil, errBadHeader(fmt.Sprintf("tag %d has count %d", tag, count))
		}
		if off%uint32(typeAlign[typ]) != 0 || off >= dl {
			return nil, errBadHeader(fmt.Sprintf("tag %d has offset %d", tag, off))
		}
		if end > off {
			return nil, errBadHeader(fmt.Sprintf("tag %d overlaps the tag before it", tag))
		}
		n, ok := dataLen(typ, data[off:], count)
		if !ok {
			return nil, errBadHeader(fmt.Sprintf("tag %d runs past the data", tag))
		}
		end = off + uint32(n)
		h.entries = append(h.entries, entry{Tag: tag, Type: typ, Count: count, Data: data[off:end]})
	}
	slices.SortStableFunc(h.entries, func(a, b entry) int { return int(a.Tag) - int(b.Tag) })
	return h, nil
}

// dataLen is the length of count items of typ at the start of d, false when they do not fit.
func dataLen(typ uint32, d []byte, count uint32) (int, bool) {
	if s := typeSize[typ]; s > 0 {
		n := int64(s) * int64(count)
		return int(n), n <= int64(len(d))
	}
	if typ == typeString && count != 1 {
		return 0, false
	}
	n := 0
	for range count {
		i := bytes.IndexByte(d[n:], 0)
		if i < 0 {
			return 0, false
		}
		n += i + 1
	}
	return n, true
}

// get returns the entry of tag.
func (h *header) get(tag uint32) (entry, bool) {
	i, ok := slices.BinarySearchFunc(h.entries, tag, func(e entry, t uint32) int { return int(e.Tag) - int(t) })
	if !ok {
		return entry{}, false
	}
	return h.entries[i], true
}

// has reports whether the header holds tag.
func (h *header) has(tag uint32) bool {
	_, ok := h.get(tag)
	return ok
}

// strs returns the strings of a string, string array or i18n string tag; for i18n strings the
// first is the default (C) locale's.
func (h *header) strs(tag uint32) []string {
	e, ok := h.get(tag)
	if !ok || (e.Type != typeString && e.Type != typeStringArray && e.Type != typeI18NString) {
		return nil
	}
	out := strings.Split(string(e.Data[:len(e.Data)-1]), "\x00")
	return out
}

// str returns the first string of a string tag, "" when there is none.
func (h *header) str(tag uint32) string {
	if s := h.strs(tag); len(s) > 0 {
		return s[0]
	}
	return ""
}

// ints returns the numbers of an integer tag.
func (h *header) ints(tag uint32) []uint64 {
	e, ok := h.get(tag)
	if !ok {
		return nil
	}
	out := make([]uint64, 0, e.Count)
	for i := range int(e.Count) {
		switch e.Type {
		case typeChar, typeInt8:
			out = append(out, uint64(e.Data[i]))
		case typeInt16:
			out = append(out, uint64(binary.BigEndian.Uint16(e.Data[2*i:])))
		case typeInt32:
			out = append(out, uint64(binary.BigEndian.Uint32(e.Data[4*i:])))
		case typeInt64:
			out = append(out, binary.BigEndian.Uint64(e.Data[8*i:]))
		default:
			return nil
		}
	}
	return out
}

// num returns the first number of an integer tag, and whether there is one.
func (h *header) num(tag uint32) (uint64, bool) {
	if n := h.ints(tag); len(n) > 0 {
		return n[0], true
	}
	return 0, false
}

// set replaces or adds the entry of e.Tag.
func (h *header) set(e entry) {
	i, ok := slices.BinarySearchFunc(h.entries, e.Tag, func(x entry, t uint32) int { return int(x.Tag) - int(t) })
	if ok {
		h.entries[i] = e
		return
	}
	h.entries = slices.Insert(h.entries, i, e)
}

// del removes the entries of tags.
func (h *header) del(tags ...uint32) {
	h.entries = slices.DeleteFunc(h.entries, func(e entry) bool { return slices.Contains(tags, e.Tag) })
}

// encode returns the bytes of the header with the region tag region, from the magic on: every
// entry in the region, in tag order, the data in the same order with each type's alignment, and
// the trailer at the end of the data.
func (h *header) encode(region uint32) []byte {
	var data []byte
	index := make([]byte, 0, (len(h.entries)+1)*entrySize)
	for _, e := range h.entries {
		for len(data)%typeAlign[e.Type] != 0 {
			data = append(data, 0)
		}
		index = binary.BigEndian.AppendUint32(index, e.Tag)
		index = binary.BigEndian.AppendUint32(index, e.Type)
		index = binary.BigEndian.AppendUint32(index, uint32(len(data)))
		index = binary.BigEndian.AppendUint32(index, e.Count)
		data = append(data, e.Data...)
	}
	il := uint32(len(h.entries) + 1)
	trailerOff := uint32(len(data))
	data = binary.BigEndian.AppendUint32(data, region)
	data = binary.BigEndian.AppendUint32(data, typeBin)
	data = binary.BigEndian.AppendUint32(data, uint32(-int32(il*entrySize)))
	data = binary.BigEndian.AppendUint32(data, regionSize)

	out := make([]byte, 0, headerIntro+int(il)*entrySize+len(data))
	out = append(out, headerMagic...)
	out = binary.BigEndian.AppendUint32(out, il)
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	out = binary.BigEndian.AppendUint32(out, region)
	out = binary.BigEndian.AppendUint32(out, typeBin)
	out = binary.BigEndian.AppendUint32(out, trailerOff)
	out = binary.BigEndian.AppendUint32(out, regionSize)
	out = append(out, index...)
	return append(out, data...)
}

// pad8 is the padding after a signature header of n bytes.
func pad8(n int) int { return (8 - n%8) % 8 }

// Entry constructors for the writer and the signature header.

func strEntry(tag uint32, s string) entry {
	return entry{Tag: tag, Type: typeString, Count: 1, Data: append([]byte(s), 0)}
}

func i18nEntry(tag uint32, s string) entry {
	return entry{Tag: tag, Type: typeI18NString, Count: 1, Data: append([]byte(s), 0)}
}

func strsEntry(tag uint32, ss []string) entry {
	var d []byte
	for _, s := range ss {
		d = append(append(d, s...), 0)
	}
	return entry{Tag: tag, Type: typeStringArray, Count: uint32(len(ss)), Data: d}
}

func int32Entry(tag uint32, ns ...uint32) entry {
	var d []byte
	for _, n := range ns {
		d = binary.BigEndian.AppendUint32(d, n)
	}
	return entry{Tag: tag, Type: typeInt32, Count: uint32(len(ns)), Data: d}
}

func int16Entry(tag uint32, ns ...uint16) entry {
	var d []byte
	for _, n := range ns {
		d = binary.BigEndian.AppendUint16(d, n)
	}
	return entry{Tag: tag, Type: typeInt16, Count: uint32(len(ns)), Data: d}
}

func int64Entry(tag uint32, ns ...uint64) entry {
	var d []byte
	for _, n := range ns {
		d = binary.BigEndian.AppendUint64(d, n)
	}
	return entry{Tag: tag, Type: typeInt64, Count: uint32(len(ns)), Data: d}
}

func binEntry(tag uint32, b []byte) entry {
	return entry{Tag: tag, Type: typeBin, Count: uint32(len(b)), Data: b}
}
