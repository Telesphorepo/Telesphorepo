package repopath

// The tags that start a name key: a name that starts with a digit (a version) sorts before every
// other name, as a digit does in byte order.
const (
	nameVersion = 0x01
	nameText    = 0x02
)

// The tags of the parts of a version key. A number is above a text, and the end of a name lies
// between them: "1.0" < "1.0.1" (a number follows) and "1.0-SNAPSHOT" < "1.0" (a text follows).
const (
	keyText = 0x01
	keyEnd  = 0x02
	keyNum  = 0x03
)

// NameKey returns the key that orders the children of one folder, the folders among themselves
// and the files among themselves. A name that starts with an ASCII digit is a version: the tag
// nameVersion and its version key, so the keys compare as the versions do ("1.2" < "1.10",
// "1.0-SNAPSHOT" < "1.0"). Any other name is the tag nameText and its bytes ("acme" < "acme-b").
// The versions come first.
func NameKey(name string) []byte {
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		return appendVersionKey([]byte{nameVersion}, name)
	}
	return append([]byte{nameText}, name...)
}

// IsVersionKey reports whether name key is the key of a name that starts with a digit.
func IsVersionKey(key []byte) bool { return len(key) > 0 && key[0] == nameVersion }

// appendVersionKey appends the version key of name. The name splits into parts at ".", "-", "+"
// and "_", and the parts compare in turn: two numbers by value ("1.2" < "1.10"), two texts by
// their bytes, a number above a text, and a missing part below a number and above a text, so
// "1.0-SNAPSHOT" < "1.0" < "1.0.1". Names that compare the same that way ("1.0" and "1-0", "01"
// and "1") follow by their bytes, so each name has a key of its own.
func appendVersionKey(out []byte, name string) []byte {
	start := 0
	for i := 0; i <= len(name); i++ {
		if i < len(name) && !isSep(name[i]) {
			continue
		}
		out = appendPart(out, name[start:i])
		start = i + 1
	}
	out = append(out, keyEnd, 0x00)
	return append(out, name...)
}

func isSep(c byte) bool { return c == '.' || c == '-' || c == '+' || c == '_' }

// appendPart appends the key of one part: an empty part as the end of a name, a number as its
// count of significant digits and those digits, a text as its bytes and a 0 byte (no name holds
// one, so a text sorts before every longer text it starts).
func appendPart(out []byte, part string) []byte {
	if part == "" {
		return append(out, keyEnd)
	}
	for i := 0; i < len(part); i++ {
		if part[i] < '0' || part[i] > '9' {
			out = append(out, keyText)
			out = append(out, part...)
			return append(out, 0x00)
		}
	}
	i := 0
	for i < len(part)-1 && part[i] == '0' {
		i++
	}
	digits := part[i:]
	if digits == "0" {
		digits = ""
	}
	out = append(out, keyNum, byte(min(len(digits), 255)))
	return append(out, digits...)
}
