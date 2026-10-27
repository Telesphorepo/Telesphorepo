package rpm

import (
	"io"
	"strconv"
	"strings"
)

// The repodata files, as createrepo_c writes them (src/xml_file.c, xml_dump_*.c,
// xml_dump_repomd.c): primary, filelists and other, each one XML document listing every package.

const (
	nsCommon    = "http://linux.duke.edu/metadata/common"
	nsRPM       = "http://linux.duke.edu/metadata/rpm"
	nsFilelists = "http://linux.duke.edu/metadata/filelists"
	nsOther     = "http://linux.duke.edu/metadata/other"
	nsRepo      = "http://linux.duke.edu/metadata/repo"
	xmlDecl     = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
)

// indexRow is one package of the index: the columns of the stored file and the parts.
type indexRow struct {
	Name, Arch, Version, Release string
	Epoch                        int64
	SHA256                       string
	Size                         int64 // of the stored file
	FileTime, BuildTime          int64
	InstalledSize, ArchiveSize   int64
	Location                     string
	HeaderStart, HeaderEnd       int64
	parts
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// epochAttr is the epoch of a package's version element: 0 without one.
func (r *indexRow) epochAttr() string { return itoa(max(r.Epoch, 0)) }

func (r *indexRow) versionElem() string {
	return `<version epoch="` + r.epochAttr() + `" ver="` + esc(r.Version) + `" rel="` + esc(r.Release) + `"/>`
}

// primaryHead, filelistsHead and otherHead open the documents of n packages.
func primaryHead(n int) string {
	return xmlDecl + `<metadata xmlns="` + nsCommon + `" xmlns:rpm="` + nsRPM + `" packages="` + strconv.Itoa(n) + `">` + "\n"
}

func filelistsHead(n int) string {
	return xmlDecl + `<filelists xmlns="` + nsFilelists + `" packages="` + strconv.Itoa(n) + `">` + "\n"
}

func otherHead(n int) string {
	return xmlDecl + `<otherdata xmlns="` + nsOther + `" packages="` + strconv.Itoa(n) + `">` + "\n"
}

const (
	primaryTail   = "</metadata>\n"
	filelistsTail = "</filelists>\n"
	otherTail     = "</otherdata>\n"
)

// writePrimary writes the primary entry of r.
func writePrimary(w io.Writer, r *indexRow) error {
	var b strings.Builder
	b.Grow(512 + len(r.Text) + len(r.FormatA) + len(r.FormatB))
	b.WriteString(`<package type="rpm">` + "\n  <name>" + esc(r.Name) + "</name>\n  <arch>" + esc(r.Arch) + "</arch>\n  ")
	b.WriteString(r.versionElem())
	b.WriteString("\n  <checksum type=\"sha256\" pkgid=\"YES\">" + r.SHA256 + "</checksum>\n  ")
	b.WriteString(r.Text)
	b.WriteString("\n  <time file=\"" + itoa(r.FileTime) + "\" build=\"" + itoa(r.BuildTime) + "\"/>")
	b.WriteString("\n  <size package=\"" + itoa(r.Size) + "\" installed=\"" + itoa(r.InstalledSize) + "\" archive=\"" + itoa(r.ArchiveSize) + "\"/>")
	b.WriteString("\n  <location href=\"" + esc(r.Location) + "\"/>\n  <format>\n    ")
	b.WriteString(r.FormatA)
	b.WriteString("\n    <rpm:header-range start=\"" + itoa(r.HeaderStart) + "\" end=\"" + itoa(r.HeaderEnd) + "\"/>")
	b.WriteString(r.FormatB)
	b.WriteString("\n  </format>\n</package>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// writeFilelists writes the filelists entry of r.
func writeFilelists(w io.Writer, r *indexRow) error {
	s := `<package pkgid="` + r.SHA256 + `" name="` + esc(r.Name) + `" arch="` + esc(r.Arch) + "\">\n  " +
		r.versionElem() + r.Files + "\n</package>\n"
	_, err := io.WriteString(w, s)
	return err
}

// writeOther writes the other entry of r.
func writeOther(w io.Writer, r *indexRow) error {
	s := `<package pkgid="` + r.SHA256 + `" name="` + esc(r.Name) + `" arch="` + esc(r.Arch) + "\">\n  " +
		r.versionElem() + r.Other + "\n</package>\n"
	_, err := io.WriteString(w, s)
	return err
}

// repomdData is one <data> of repomd.xml.
type repomdData struct {
	Type         string
	Checksum     string // of the compressed file
	OpenChecksum string // of the XML
	Href         string
	Timestamp    int64
	Size         int64
	OpenSize     int64
}

// repomdXML is repomd.xml for the revision rev and the files ds.
func repomdXML(rev int64, ds []repomdData) []byte {
	var b strings.Builder
	b.WriteString(xmlDecl + `<repomd xmlns="` + nsRepo + `" xmlns:rpm="` + nsRPM + "\">\n  <revision>" + itoa(rev) + "</revision>\n")
	for _, d := range ds {
		b.WriteString(`  <data type="` + d.Type + "\">\n")
		b.WriteString(`    <checksum type="sha256">` + d.Checksum + "</checksum>\n")
		b.WriteString(`    <open-checksum type="sha256">` + d.OpenChecksum + "</open-checksum>\n")
		b.WriteString(`    <location href="` + d.Href + "\"/>\n")
		b.WriteString("    <timestamp>" + itoa(d.Timestamp) + "</timestamp>\n")
		b.WriteString("    <size>" + itoa(d.Size) + "</size>\n")
		b.WriteString("    <open-size>" + itoa(d.OpenSize) + "</open-size>\n")
		b.WriteString("  </data>\n")
	}
	b.WriteString("</repomd>\n")
	return []byte(b.String())
}
