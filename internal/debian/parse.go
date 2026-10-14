package debian

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"
	"pault.ag/go/debian/control"
	"pault.ag/go/debian/deb"
	"pault.ag/go/debian/version"
)

const (
	maxControlTar  = 16 << 20
	maxControlFile = 1 << 20
	maxArMembers   = 16 // a .deb has 3; bounds the member walk
)

func init() { deb.SetXZMaxDict(64 << 20) }

var (
	packageRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	archRE      = regexp.MustCompile(`^[a-z0-9-]+$`)
	versionRE   = regexp.MustCompile(`^[A-Za-z0-9.+~:-]+$`)
	fieldNameRE = regexp.MustCompile(`^[!-"$-,.-9;-~][!-9;-~]*$`)
)

// droppedFields are recomputed by the server and never taken from the uploaded control file.
var droppedFields = []string{"Filename", "Size", "MD5sum", "SHA1", "SHA256", "SHA512"}

// badDeb is a client error (400) in the uploaded package.
type badDeb string

func (e badDeb) Error() string { return string(e) }

type debInfo struct {
	Package, Version, Architecture string
	Source                         string // source package name (no version)
	VersionNoEpoch                 string
	Control                        string // re-serialized stanza, each line ending in "\n"
}

// poolPath implements spec §5.1.
func (d *debInfo) poolPath(component string) string {
	prefix := d.Source[:1]
	if strings.HasPrefix(d.Source, "lib") && len(d.Source) > 3 {
		prefix = d.Source[:4]
	}
	return "pool/" + component + "/" + prefix + "/" + d.Source + "/" +
		d.Package + "_" + d.VersionNoEpoch + "_" + d.Architecture + ".deb"
}

// validKey reports whether a storage key derived from untrusted fields satisfies the
// spec §6 limits (non-empty segments of at most 255 bytes, whole key at most 1024).
func validKey(k string) bool {
	if k == "" || len(k) > 1024 {
		return false
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > 255 {
			return false
		}
	}
	return true
}

// parseDeb reads only the control member of a .deb; data.tar is never touched.
func parseDeb(f io.ReaderAt) (*debInfo, error) {
	ar, err := deb.LoadAr(f)
	if err != nil {
		return nil, badDeb("not a .deb (ar) archive")
	}
	for range maxArMembers {
		e, err := ar.Next()
		if err == io.EOF {
			return nil, badDeb("missing control.tar member")
		}
		if err != nil {
			return nil, badDeb("malformed ar archive")
		}
		// deb.Ar accepts negative sizes, which would stall or rewind its offset.
		if e.Size < 0 {
			return nil, badDeb("malformed ar archive")
		}
		name := strings.TrimRight(e.Name, " /")
		if name != "control.tar" && !strings.HasPrefix(name, "control.tar.") {
			continue
		}
		ext := strings.TrimPrefix(name, "control.tar")
		switch ext {
		case "", ".gz", ".xz", ".zst":
		default:
			return nil, badDeb("unsupported control.tar compression " + ext)
		}
		data, err := readControlFile(e.Data, ext)
		if err != nil {
			return nil, err
		}
		return parseControl(data)
	}
	return nil, badDeb("missing control.tar member")
}

func readControlFile(r io.Reader, ext string) ([]byte, error) {
	var dec io.ReadCloser
	var err error
	if ext == ".zst" {
		// Constructed directly so the decoder can be closed (deb.DecompressorFor wraps it in
		// a NopCloser, leaking its goroutines) and its memory bounded.
		var z *zstd.Decoder
		z, err = zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxWindow(64<<20), zstd.WithDecoderMaxMemory(maxControlTar+1))
		if err == nil {
			dec = z.IOReadCloser()
		}
	} else {
		dec, err = deb.DecompressorFor(ext)(r)
	}
	if err != nil {
		return nil, badDeb("cannot decompress control.tar")
	}
	defer dec.Close()

	lr := &io.LimitedReader{R: dec, N: maxControlTar + 1}
	tooBig := badDeb(fmt.Sprintf("control.tar exceeds %d bytes decompressed", maxControlTar))
	tr := tar.NewReader(lr)
	for {
		hdr, err := tr.Next()
		if lr.N <= 0 {
			return nil, tooBig
		}
		if err == io.EOF {
			return nil, badDeb("control.tar has no control file")
		}
		if err != nil {
			return nil, badDeb("malformed control.tar")
		}
		if n := strings.TrimPrefix(hdr.Name, "./"); n != "control" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size > maxControlFile {
			return nil, badDeb("control file too large")
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxControlFile+1))
		if lr.N <= 0 {
			return nil, tooBig
		}
		if err != nil {
			return nil, badDeb("malformed control.tar")
		}
		if len(data) > maxControlFile {
			return nil, badDeb("control file too large")
		}
		return data, nil
	}
}

func parseControl(data []byte) (*debInfo, error) {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN PGP")) {
		return nil, badDeb("signed control files are not supported")
	}
	pr, err := control.NewParagraphReader(bytes.NewReader(data), nil)
	if err != nil {
		return nil, badDeb("malformed control file")
	}
	para, err := pr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, badDeb("empty control file")
		}
		return nil, badDeb("malformed control file: " + err.Error())
	}

	seen := make(map[string]bool, len(para.Order))
	for _, k := range para.Order {
		if !fieldNameRE.MatchString(k) {
			return nil, badDeb(fmt.Sprintf("invalid control field name %q", k))
		}
		lk := strings.ToLower(k)
		if seen[lk] {
			return nil, badDeb("duplicate control field " + k)
		}
		seen[lk] = true
	}
	get := func(name string) string {
		for _, k := range para.Order {
			if strings.EqualFold(k, name) {
				return strings.TrimSpace(para.Values[k])
			}
		}
		return ""
	}

	d := &debInfo{Package: get("Package"), Version: get("Version"), Architecture: get("Architecture")}
	if !packageRE.MatchString(d.Package) {
		return nil, badDeb("invalid or missing Package field")
	}
	if !versionRE.MatchString(d.Version) {
		return nil, badDeb("invalid or missing Version field")
	}
	v, err := version.Parse(d.Version)
	if err != nil {
		return nil, badDeb("invalid Version: " + err.Error())
	}
	d.VersionNoEpoch = v.StringWithoutEpoch()
	if d.VersionNoEpoch == "" || strings.ContainsAny(d.VersionNoEpoch, "/") {
		return nil, badDeb("invalid Version field")
	}
	if !archRE.MatchString(d.Architecture) {
		return nil, badDeb("invalid or missing Architecture field")
	}
	d.Source = d.Package
	if src := strings.Fields(get("Source")); len(src) > 0 {
		d.Source = src[0] // "Source: name (version)" → name
		if !packageRE.MatchString(d.Source) {
			return nil, badDeb("invalid Source field")
		}
	}
	d.Control = serializeControl(para)
	return d, nil
}

// serializeControl writes fields in their original order, minus droppedFields, with
// multi-line values as continuation lines (" ." for empty lines).
func serializeControl(p *control.Paragraph) string {
	var b strings.Builder
	for _, k := range p.Order {
		if isDropped(k) {
			continue
		}
		lines := strings.Split(strings.TrimRight(p.Values[k], "\n"), "\n")
		b.WriteString(k)
		b.WriteByte(':')
		if first := strings.TrimSpace(lines[0]); first != "" {
			b.WriteByte(' ')
			b.WriteString(first)
		}
		b.WriteByte('\n')
		for _, l := range lines[1:] {
			l = strings.TrimRight(l, " \t\r")
			if l == "" {
				b.WriteString(" .\n")
				continue
			}
			b.WriteByte(' ')
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func isDropped(k string) bool {
	for _, f := range droppedFields {
		if strings.EqualFold(k, f) {
			return true
		}
	}
	return false
}
