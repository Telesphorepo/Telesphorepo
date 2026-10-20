package pypi

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"path"
	"regexp"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Python distribution files: the name rules of PEP 503 and PEP 508, the file names of wheels
// (PEP 427) and sdists (PEP 625), and the core metadata inside them (*.dist-info/METADATA of a
// wheel, PKG-INFO in the top folder of an sdist).

// nameRE is a valid project name (PEP 508), in any case.
var nameRE = regexp.MustCompile(`(?i)^([a-z0-9]|[a-z0-9][a-z0-9._-]*[a-z0-9])$`)

var separators = regexp.MustCompile(`[-_.]+`)

// normalize returns the normalised form of a project name (PEP 503): lower case, each run of
// "-", "_" and "." one "-".
func normalize(name string) string { return strings.ToLower(separators.ReplaceAllString(name, "-")) }

// validName reports whether name is a project name.
func validName(name string) bool { return len(name) <= 200 && nameRE.MatchString(name) }

// File kinds.
const (
	kindWheel = "wheel"
	kindSdist = "sdist"
)

// distFile is what a distribution file name says.
type distFile struct {
	kind    string
	name    string         // the name as written in the file name
	version version.PEP440 // the version of the file name
	tags    string         // a wheel's "<python>-<abi>-<platform>"; "" for an sdist
	ext     string         // "whl", "tar.gz" or "zip"
}

// sdistExts are the extensions of an sdist that the server takes.
var sdistExts = []string{".tar.gz", ".zip"}

// parseFilename reads a wheel or sdist file name.
func parseFilename(filename string) (distFile, error) {
	if filename == "" || strings.ContainsAny(filename, "/\\") || len(filename) > 255 {
		return distFile{}, errors.New("invalid file name")
	}
	if stem, ok := strings.CutSuffix(filename, ".whl"); ok {
		parts := strings.Split(stem, "-")
		if len(parts) != 5 && len(parts) != 6 {
			return distFile{}, fmt.Errorf("%s is not a wheel file name: <name>-<version>[-<build>]-<python>-<abi>-<platform>.whl", filename)
		}
		v, err := version.ParsePEP440(parts[1])
		if err != nil || !validName(parts[0]) {
			return distFile{}, fmt.Errorf("%s is not a wheel file name: the name or the version does not parse", filename)
		}
		n := len(parts)
		return distFile{kind: kindWheel, name: parts[0], version: v, tags: strings.Join(parts[n-3:], "-"), ext: "whl"}, nil
	}
	for _, ext := range sdistExts {
		stem, ok := strings.CutSuffix(filename, ext)
		if !ok {
			continue
		}
		// A legacy sdist has "-" in its name too: the version is after the last "-" that leaves
		// a version.
		for i := strings.LastIndexByte(stem, '-'); i > 0; i = strings.LastIndexByte(stem[:i], '-') {
			v, err := version.ParsePEP440(stem[i+1:])
			if err == nil && validName(stem[:i]) {
				return distFile{kind: kindSdist, name: stem[:i], version: v, ext: ext[1:]}, nil
			}
		}
		return distFile{}, fmt.Errorf("%s is not an sdist file name: <name>-<version>%s", filename, ext)
	}
	return distFile{}, fmt.Errorf("%s is not a wheel (.whl) or an sdist (.tar.gz, .zip)", filename)
}

// maxMetadata is the largest core metadata file that the server reads.
const maxMetadata = 16 << 20

// coreMetadata is the part of the core metadata that the server keeps.
type coreMetadata struct {
	raw            []byte // the file as it is in the distribution
	name           string
	version        version.PEP440
	requiresPython string
	summary        string
}

// badDist is a distribution file that the server refuses (400).
type badDist struct{ msg string }

func (e badDist) Error() string { return e.msg }

func bad(format string, args ...any) error { return badDist{fmt.Sprintf(format, args...)} }

// readerAt returns the content of sp for the archive readers.
func readerAt(sp *assets.Spooled) io.ReaderAt {
	if sp.File != nil {
		return sp.File
	}
	return bytes.NewReader(sp.Data)
}

// readMetadata reads the core metadata of the distribution file f: the METADATA of the wheel's
// .dist-info folder, or the PKG-INFO in the top folder of an sdist.
func readMetadata(f distFile, sp *assets.Spooled) (*coreMetadata, error) {
	var raw []byte
	var err error
	switch {
	case f.kind == kindWheel:
		raw, err = zipMember(sp, func(name string) bool {
			dir, file, ok := strings.Cut(name, "/")
			return ok && file == "METADATA" && strings.HasSuffix(dir, ".dist-info")
		}, "a .dist-info/METADATA")
	case f.ext == "zip":
		raw, err = zipMember(sp, isPKGInfo, "a PKG-INFO in its top folder")
	default:
		raw, err = tarMember(sp, isPKGInfo)
	}
	if err != nil {
		return nil, err
	}
	return parseMetadata(raw)
}

// isPKGInfo reports whether an archive member is the PKG-INFO of the top folder.
func isPKGInfo(name string) bool {
	_, file, ok := strings.Cut(strings.TrimPrefix(name, "./"), "/")
	return ok && file == "PKG-INFO"
}

// zipMember returns the one member of the zip in sp that match accepts.
func zipMember(sp *assets.Spooled, match func(string) bool, what string) ([]byte, error) {
	zr, err := zip.NewReader(readerAt(sp), sp.Size)
	if err != nil {
		return nil, bad("the file is not a zip archive")
	}
	var found *zip.File
	for _, zf := range zr.File {
		if match(zf.Name) {
			if found != nil {
				return nil, bad("the archive has more than one %s", strings.TrimPrefix(what, "a "))
			}
			found = zf
		}
	}
	if found == nil {
		return nil, bad("the archive has no %s", strings.TrimPrefix(what, "a "))
	}
	if found.UncompressedSize64 > maxMetadata {
		return nil, bad("the core metadata is larger than %d MiB", maxMetadata>>20)
	}
	rc, err := found.Open()
	if err != nil {
		return nil, bad("the core metadata does not open: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxMetadata+1))
	if err != nil {
		return nil, bad("the core metadata does not read: %v", err)
	}
	return b, nil
}

// tarMember returns the first member of the gzipped tar in sp that match accepts.
func tarMember(sp *assets.Spooled, match func(string) bool) ([]byte, error) {
	r, err := sp.Reader()
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, bad("the file is not a gzip archive")
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, bad("the archive has no PKG-INFO in its top folder")
		}
		if err != nil {
			return nil, bad("the archive does not read: %v", err)
		}
		if h.Typeflag != tar.TypeReg || !match(h.Name) {
			continue
		}
		if h.Size > maxMetadata {
			return nil, bad("the core metadata is larger than %d MiB", maxMetadata>>20)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, bad("the archive does not read: %v", err)
		}
		return b, nil
	}
}

// parseMetadata reads the header fields of a core metadata file (RFC 822 style).
func parseMetadata(raw []byte) (*coreMetadata, error) {
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw))).ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, bad("the core metadata does not parse: %v", err)
	}
	m := &coreMetadata{raw: raw, name: strings.TrimSpace(h.Get("Name")),
		requiresPython: strings.TrimSpace(h.Get("Requires-Python")), summary: strings.TrimSpace(h.Get("Summary"))}
	if !validName(m.name) {
		return nil, bad("the core metadata has no valid Name")
	}
	if m.version, err = version.ParsePEP440(strings.TrimSpace(h.Get("Version"))); err != nil {
		return nil, bad("the core metadata has no valid Version")
	}
	return m, nil
}

// check refuses metadata m that names another project or version than the file name f.
func (m *coreMetadata) check(f distFile, filename string) error {
	if normalize(m.name) != normalize(f.name) {
		return bad("%s names project %s, but its metadata names %s", filename, f.name, m.name)
	}
	if m.version.String() != f.version.String() {
		return bad("%s names version %s, but its metadata names %s", filename, f.version, m.version)
	}
	return nil
}

// ext returns the extension of a distribution file name for the version browser.
func extOf(filename string) string {
	for _, e := range sdistExts {
		if strings.HasSuffix(filename, e) {
			return e[1:]
		}
	}
	return strings.TrimPrefix(path.Ext(filename), ".")
}
