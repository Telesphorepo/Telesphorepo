package arch

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/xi2/xz"
)

// The limits of the parse of an uploaded package. A package is a compressed tar; listing its
// files means reading it whole, so these bound what an upload makes the server decompress.
const (
	maxPkgInfo     = 1 << 20  // the .PKGINFO file
	maxEntries     = 200_000  // tar entries
	maxPathBytes   = 64 << 20 // the bytes of every path together
	maxRatio       = 100      // decompressed bytes per compressed byte...
	minDecompBound = 1 << 30  // ...but at least this many
)

var (
	nameRE    = regexp.MustCompile(`^[a-z0-9@_+][a-z0-9@._+-]{0,127}$`)
	pkgverRE  = regexp.MustCompile(`^[A-Za-z0-9._+~]{1,128}$`)
	pkgrelRE  = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,9})?$`)
	epochRE   = regexp.MustCompile(`^[0-9]{1,9}$`)
	archRE    = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	extOfZstd = []byte{0x28, 0xb5, 0x2f, 0xfd}
	extOfXz   = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	extOfGzip = []byte{0x1f, 0x8b}
)

// badPkg is a client error (400) in the uploaded package.
type badPkg string

func (e badPkg) Error() string { return string(e) }

// pkgInfo is what an upload reads of a package.
type pkgInfo struct {
	Name, Version, Arch string
	Ext                 string // the compression of the file: zst, xz or gz
	Desc                string // the desc entry of the indexes
	Files               string // the files entry of <repo>.files
	SigState            []byte // the SHA-256 state after the file's bytes
	Fields              map[string][]string
}

// fileName is the name of the package file: <name>-<version>-<arch>.pkg.tar.<ext>.
func (p *pkgInfo) fileName() string {
	return p.Name + "-" + p.Version + "-" + p.Arch + ".pkg.tar." + p.Ext
}

// poolPath is the path of the package file: pool/<name>/<file>.
func (p *pkgInfo) poolPath() string { return "pool/" + p.Name + "/" + p.fileName() }

// extOf names the compression of a package from its first bytes.
func extOf(head []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(head, extOfZstd):
		return "zst", true
	case bytes.HasPrefix(head, extOfXz):
		return "xz", true
	case bytes.HasPrefix(head, extOfGzip):
		return "gz", true
	}
	return "", false
}

// decompressor returns the decompressed stream of a package with compression ext, and its close.
func decompressor(ext string, r io.Reader) (io.Reader, func(), error) {
	switch ext {
	case "zst":
		z, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxWindow(128<<20))
		if err != nil {
			return nil, nil, err
		}
		return z, z.Close, nil
	case "xz":
		x, err := xz.NewReader(r, 0)
		return x, func() {}, err
	default:
		g, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return g, func() { g.Close() }, nil
	}
}

// parsePackage reads a whole package: .PKGINFO, the list of its files, and the SHA-256 state of
// its bytes. size is the length of r.
func parsePackage(r io.Reader, size int64) (*pkgInfo, error) {
	h := sha256.New()
	br := bufio.NewReaderSize(io.TeeReader(r, h), 64<<10)
	head, _ := br.Peek(8)
	ext, ok := extOf(head)
	if !ok {
		return nil, badPkg("not a pacman package: a .pkg.tar.zst, .pkg.tar.xz or .pkg.tar.gz file")
	}
	dec, closeDec, err := decompressor(ext, br)
	if err != nil {
		return nil, badPkg("cannot decompress the package")
	}
	defer closeDec()
	bound := max(size*maxRatio, minDecompBound)
	lr := &io.LimitedReader{R: dec, N: bound + 1}
	tr := tar.NewReader(lr)
	var info []byte
	var paths []string
	pathBytes := 0
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if lr.N <= 0 {
			return nil, badPkg(fmt.Sprintf("the package decompresses to more than %d bytes", bound))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, badPkg("the package is not a valid compressed tar archive")
		}
		if n >= maxEntries {
			return nil, badPkg(fmt.Sprintf("the package has more than %d files", maxEntries))
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == ".PKGINFO" {
			if hdr.Size > maxPkgInfo {
				return nil, badPkg(".PKGINFO is too large")
			}
			if info, err = io.ReadAll(io.LimitReader(tr, maxPkgInfo+1)); err != nil {
				return nil, badPkg("the package is not a valid compressed tar archive")
			}
			continue
		}
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if hdr.Typeflag == tar.TypeDir && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		if pathBytes += len(name) + 1; pathBytes > maxPathBytes {
			return nil, badPkg("the paths of the package's files are too long together")
		}
		paths = append(paths, name)
	}
	// The rest of the file (padding after the tar end) belongs to the hash too.
	if _, err := io.Copy(io.Discard, br); err != nil {
		return nil, err
	}
	if info == nil {
		return nil, badPkg("the package has no .PKGINFO")
	}
	p, err := parsePkgInfo(info)
	if err != nil {
		return nil, err
	}
	p.Ext = ext
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return nil, err
	}
	p.SigState = state
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var files strings.Builder
	files.WriteString("%FILES%\n")
	for _, x := range paths {
		files.WriteString(x)
		files.WriteByte('\n')
	}
	p.Files = files.String()
	return p, nil
}

// headInfo reads .PKGINFO from the first bytes of a package, for a plan. found is false when the
// bytes end before .PKGINFO.
func headInfo(head []byte) (p *pkgInfo, found bool, err error) {
	ext, ok := extOf(head)
	if !ok {
		return nil, true, badPkg("not a pacman package: a .pkg.tar.zst, .pkg.tar.xz or .pkg.tar.gz file")
	}
	dec, closeDec, err := decompressor(ext, bytes.NewReader(head))
	if err != nil {
		return nil, false, nil
	}
	defer closeDec()
	tr := tar.NewReader(io.LimitReader(dec, 64<<20))
	for range 64 {
		hdr, err := tr.Next()
		if err != nil {
			return nil, false, nil
		}
		if strings.TrimPrefix(hdr.Name, "./") != ".PKGINFO" {
			continue
		}
		info, err := io.ReadAll(io.LimitReader(tr, maxPkgInfo+1))
		if err != nil {
			return nil, false, nil
		}
		p, err := parsePkgInfo(info)
		if err != nil {
			return nil, true, err
		}
		p.Ext = ext
		return p, true, nil
	}
	return nil, false, nil
}

// listKeys are the .PKGINFO keys that may repeat, with the desc field of each.
var descOrder = []struct{ key, field string }{
	{"pkgname", "NAME"}, {"pkgbase", "BASE"}, {"pkgver", "VERSION"}, {"pkgdesc", "DESC"},
	{"group", "GROUPS"}, {"", "CSIZE"}, {"size", "ISIZE"}, {"", "SHA256SUM"},
	{"url", "URL"}, {"license", "LICENSE"}, {"arch", "ARCH"}, {"builddate", "BUILDDATE"},
	{"packager", "PACKAGER"}, {"replaces", "REPLACES"}, {"conflict", "CONFLICTS"},
	{"provides", "PROVIDES"}, {"depend", "DEPENDS"}, {"optdepend", "OPTDEPENDS"},
	{"makedepend", "MAKEDEPENDS"}, {"checkdepend", "CHECKDEPENDS"},
}

// parsePkgInfo reads .PKGINFO ("key = value" lines; # comments) and checks the name, version and
// architecture. The desc entry is finished by withFile, which adds the file name, size and hash.
func parsePkgInfo(b []byte) (*pkgInfo, error) {
	if len(b) > maxPkgInfo {
		return nil, badPkg(".PKGINFO is too large")
	}
	fields := map[string][]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Join(strings.Fields(v), " ")
		fields[k] = append(fields[k], v)
	}
	one := func(k string) string {
		if vs := fields[k]; len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	p := &pkgInfo{Name: one("pkgname"), Version: one("pkgver"), Arch: one("arch"), Fields: fields}
	if !nameRE.MatchString(p.Name) {
		return nil, badPkg(fmt.Sprintf(".PKGINFO: invalid or missing pkgname %q", p.Name))
	}
	if err := checkVersion(p.Version); err != nil {
		return nil, err
	}
	if !archRE.MatchString(p.Arch) {
		return nil, badPkg(fmt.Sprintf(".PKGINFO: invalid or missing arch %q", p.Arch))
	}
	switch t := one("pkgtype"); t {
	case "", "pkg", "split", "debug":
	default:
		return nil, badPkg(fmt.Sprintf(".PKGINFO: pkgtype %q is not an installable package", t))
	}
	return p, nil
}

// checkVersion checks a full version [epoch:]pkgver-pkgrel.
func checkVersion(v string) error {
	epoch, rest, hasEpoch := strings.Cut(v, ":")
	if !hasEpoch {
		rest, epoch = epoch, ""
	} else if !epochRE.MatchString(epoch) {
		return badPkg(fmt.Sprintf(".PKGINFO: invalid epoch in pkgver %q", v))
	}
	i := strings.LastIndexByte(rest, '-')
	if i < 0 || !pkgverRE.MatchString(rest[:i]) || !pkgrelRE.MatchString(rest[i+1:]) {
		return badPkg(fmt.Sprintf(".PKGINFO: invalid or missing pkgver %q (want [epoch:]pkgver-pkgrel)", v))
	}
	return nil
}

// withFile finishes the desc entry of p for its file of size bytes and SHA-256 sum.
func (p *pkgInfo) withFile(size int64, sum string) {
	var b strings.Builder
	entry := func(field string, vs ...string) {
		var keep []string
		for _, v := range vs {
			if v != "" {
				keep = append(keep, v)
			}
		}
		if len(keep) == 0 {
			return
		}
		b.WriteString("%" + field + "%\n")
		for _, v := range keep {
			b.WriteString(v + "\n")
		}
		b.WriteString("\n")
	}
	entry("FILENAME", p.fileName())
	for _, f := range descOrder {
		switch f.field {
		case "CSIZE":
			entry(f.field, fmt.Sprint(size))
		case "SHA256SUM":
			entry(f.field, sum)
		case "NAME", "BASE", "VERSION", "DESC", "ISIZE", "URL", "ARCH", "BUILDDATE", "PACKAGER":
			if vs := p.Fields[f.key]; len(vs) > 0 {
				entry(f.field, vs[0])
			}
		default:
			entry(f.field, p.Fields[f.key]...)
		}
	}
	p.Desc = b.String()
}

// descFields reads a desc entry back: each %FIELD% with its values.
func descFields(desc string) map[string][]string {
	out := map[string][]string{}
	var cur string
	for _, line := range strings.Split(desc, "\n") {
		switch {
		case strings.HasPrefix(line, "%") && strings.HasSuffix(line, "%") && len(line) > 2:
			cur = strings.Trim(line, "%")
		case line == "":
			cur = ""
		case cur != "":
			out[cur] = append(out[cur], line)
		}
	}
	return out
}

// errNotPackage is a path that names no package file.
var errNotPackage = errors.New("not a package file")

// splitFileName splits a package file name <name>-<pkgver>-<pkgrel>-<arch>.pkg.tar.<ext>[.sig]
// into the name and the architecture: pkgver, pkgrel and arch hold no '-', so the name is what
// comes before the last three.
func splitFileName(file string) (name, arch string, err error) {
	base := strings.TrimSuffix(file, ".sig")
	i := strings.Index(base, ".pkg.tar.")
	if i <= 0 || strings.Contains(base[i+len(".pkg.tar."):], ".") || strings.Contains(base[i+len(".pkg.tar."):], "/") || base[i+len(".pkg.tar."):] == "" {
		return "", "", errNotPackage
	}
	parts := strings.Split(base[:i], "-")
	if len(parts) < 4 {
		return "", "", errNotPackage
	}
	name = strings.Join(parts[:len(parts)-3], "-")
	arch = parts[len(parts)-1]
	if !nameRE.MatchString(name) || !archRE.MatchString(arch) {
		return "", "", errNotPackage
	}
	return name, arch, nil
}
