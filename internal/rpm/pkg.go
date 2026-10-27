package rpm

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// maxHeaderData is the most data a package's header may hold (rpm's own limit is 256 MiB; the
// largest Fedora headers hold a few MiB).
const maxHeaderData = 32 << 20

// maxFiles is the most files a package may list.
const maxFiles = 200_000

var (
	nameRE    = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9._+-]{0,127}$`)
	versionRE = regexp.MustCompile(`^[A-Za-z0-9._+~^]{1,128}$`)
	archRE    = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
)

// badPkg is a client error (400) in the uploaded package.
type badPkg string

func (e badPkg) Error() string { return string(e) }

// pkgInfo is what an upload reads from a package: its identity, the facts of the index entry and
// where its header lies.
type pkgInfo struct {
	Name, Version, Release, Arch string
	Epoch                        int64 // -1: the package has no epoch
	Format                       int   // RPMFORMAT: 4, or 6 for an rpm 6 package
	Summary                      string
	BuildTime                    int64
	InstalledSize, ArchiveSize   int64

	sig           *header // the signature header of the upload
	hdr           *header
	headerStart   int64  // where the header starts in the upload
	headerLen     int64  // the header's bytes, from its magic on
	hdrState      []byte // the SHA-256 state after the header's bytes
	payloadAlgo   int
	payloadDigest string

	parts parts
}

// parts are the pieces of the package's index entries that do not depend on the stored file.
type parts struct {
	Text    string // <summary> to <url> of the primary entry
	FormatA string // <rpm:license> to <rpm:sourcerpm>
	FormatB string // the dependencies and the primary files
	Files   string // the <file> elements of the filelists entry
	Other   string // the <changelog> elements of the other entry
}

// epochNum is the epoch of the version order: 0 when the package has none.
func (p *pkgInfo) epochNum() int64 { return max(p.Epoch, 0) }

// evr is the version as dnf shows it: [epoch:]version-release, without an epoch of 0.
func (p *pkgInfo) evr() string { return evrString(p.Epoch, p.Version, p.Release) }

func evrString(epoch int64, version, release string) string {
	v := version + "-" + release
	if epoch > 0 {
		return strconv.FormatInt(epoch, 10) + ":" + v
	}
	return v
}

// fileName is rpm's file name of the package: <name>-<version>-<release>.<arch>.rpm.
func (p *pkgInfo) fileName() string {
	return p.Name + "-" + p.Version + "-" + p.Release + "." + p.Arch + ".rpm"
}

// path is where the repo keeps the package.
func (p *pkgInfo) path() string { return "Packages/" + p.Name + "/" + p.fileName() }

// readPackage reads the lead and both headers of the package in r (the start of the file); it
// does not read the payload. A client error is a badPkg.
func readPackage(r io.Reader) (*pkgInfo, error) {
	cr := &countReader{r: r}
	lead := make([]byte, leadSize)
	if _, err := io.ReadFull(cr, lead); err != nil || !bytes.Equal(lead[:4], leadMagic) {
		return nil, badPkg("not an RPM package")
	}
	sig, sigRaw, err := readHeader(cr, maxSigDataSize)
	if err != nil {
		return nil, badPkg("signature header: " + err.Error())
	}
	if _, err := io.CopyN(io.Discard, cr, int64(pad8(len(sigRaw)))); err != nil {
		return nil, badPkg("the file ends after its signature header")
	}
	start := cr.n
	hdr, hdrRaw, err := readHeader(cr, maxHeaderData)
	if err != nil {
		return nil, badPkg(err.Error())
	}
	if want := sig.str(sigTagSHA256); want != "" {
		sum := sha256.Sum256(hdrRaw)
		if hex.EncodeToString(sum[:]) != strings.ToLower(want) {
			return nil, badPkg("the header does not match its SHA-256 digest: the package is damaged")
		}
	}
	h := sha256.New()
	h.Write(hdrRaw)
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return nil, err
	}
	p := &pkgInfo{sig: sig, hdr: hdr, headerStart: start, headerLen: int64(len(hdrRaw)), hdrState: state}
	if err := p.read(); err != nil {
		return nil, err
	}
	return p, nil
}

// countReader counts the bytes read through it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// read fills the fields and the index parts from the headers and checks them.
func (p *pkgInfo) read() error {
	h := p.hdr
	p.Name, p.Version, p.Release = h.str(tagName), h.str(tagVersion), h.str(tagRelease)
	p.Arch = h.str(tagArch)
	if !nameRE.MatchString(p.Name) {
		return badPkg(fmt.Sprintf("invalid or missing package name %q", p.Name))
	}
	if !versionRE.MatchString(p.Version) {
		return badPkg(fmt.Sprintf("invalid or missing version %q", p.Version))
	}
	if !versionRE.MatchString(p.Release) {
		return badPkg(fmt.Sprintf("invalid or missing release %q", p.Release))
	}
	if h.has(tagSourcePackage) || p.Arch == "src" || p.Arch == "nosrc" {
		return badPkg("a source package (.src.rpm) is not installable; upload binary packages")
	}
	if !archRE.MatchString(p.Arch) {
		return badPkg(fmt.Sprintf("invalid or missing architecture %q", p.Arch))
	}
	p.Epoch = -1
	if e, ok := h.num(tagEpoch); ok {
		if e > 1<<31-1 {
			return badPkg("invalid epoch")
		}
		p.Epoch = int64(e)
	}
	p.Format = 4
	if f, ok := h.num(tagRPMFormat); ok {
		p.Format = int(f)
	}
	digests := h.strs(tagPayloadDigest)
	if len(digests) == 0 || digests[0] == "" {
		return badPkg("the package has no payload digest: rebuild it with rpm 4.14 or later, or nfpm")
	}
	p.payloadDigest = strings.ToLower(digests[0])
	p.payloadAlgo = hashSHA256
	if a, ok := h.num(tagPayloadDigestAlgo); ok {
		p.payloadAlgo = int(a)
	}
	if p.payloadAlgo != hashSHA256 && p.payloadAlgo != hashSHA512 {
		return badPkg(fmt.Sprintf("payload digest algorithm %d is not SHA-256 or SHA-512", p.payloadAlgo))
	}
	p.Summary = h.str(tagSummary)
	if t, ok := h.num(tagBuildTime); ok {
		p.BuildTime = int64(t)
	}
	if n, ok := h.num(tagLongSize); ok {
		p.InstalledSize = int64(n)
	} else if n, ok := h.num(tagSize); ok {
		p.InstalledSize = int64(n)
	}
	switch {
	case h.has(tagPayloadSizeAlt):
		n, _ := h.num(tagPayloadSizeAlt)
		p.ArchiveSize = int64(n)
	case p.sig.has(sigTagLongArchiveSize):
		n, _ := p.sig.num(sigTagLongArchiveSize)
		p.ArchiveSize = int64(n)
	case p.sig.has(sigTagPayloadSize):
		n, _ := p.sig.num(sigTagPayloadSize)
		p.ArchiveSize = int64(n)
	default:
		n, _ := h.num(tagArchiveSize)
		p.ArchiveSize = int64(n)
	}
	files, err := p.files()
	if err != nil {
		return err
	}
	p.parts = p.render(files)
	return nil
}

// pkgFile is one file of a package.
type pkgFile struct {
	path string
	typ  string // "", "dir" or "ghost"
}

// files returns the files of the package from its header: each basename with its directory, a
// folder "dir", a %ghost file "ghost" (createrepo_c's parsehdr.c).
func (p *pkgInfo) files() ([]pkgFile, error) {
	h := p.hdr
	base, dirs := h.strs(tagBaseNames), h.strs(tagDirNames)
	idx, modes, flags := h.ints(tagDirIndexes), h.ints(tagFileModes), h.ints(tagFileFlags)
	if len(base) == 0 {
		return nil, nil
	}
	if len(base) > maxFiles {
		return nil, badPkg(fmt.Sprintf("the package has more than %d files", maxFiles))
	}
	if len(idx) != len(base) || len(modes) != len(base) || len(flags) != len(base) {
		return nil, badPkg("the file list of the header is inconsistent")
	}
	out := make([]pkgFile, len(base))
	for i, b := range base {
		d := ""
		if int(idx[i]) < len(dirs) {
			d = dirs[idx[i]]
		}
		f := pkgFile{path: d + b}
		if hasControl(f.path) {
			return nil, badPkg("a file path holds a control character")
		}
		switch {
		case modes[i]&modeType == modeDir:
			f.typ = "dir"
		case flags[i]&fileGhost != 0:
			f.typ = "ghost"
		}
		out[i] = f
	}
	return out, nil
}

// hasControl reports whether s holds a control character other than tab, newline and return
// (createrepo_c refuses those in identifiers and paths).
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 32 && c != 9 && c != 10 && c != 13 {
			return true
		}
	}
	return false
}

// isPrimaryFile reports whether a file goes into the primary entry (createrepo_c's
// cr_is_primary): under /etc/, /usr/lib/sendmail, or any path holding "bin/".
func isPrimaryFile(f string) bool {
	return strings.HasPrefix(f, "/etc/") || f == "/usr/lib/sendmail" || strings.Contains(f, "bin/")
}
