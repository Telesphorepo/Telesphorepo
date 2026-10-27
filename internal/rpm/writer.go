package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// buildSpec is a package that writeRPM builds: the demo data and the seeds use it. The result is a
// version 4 package with a gzip cpio payload that rpm installs, unsigned (the upload signs it).
type buildSpec struct {
	Name, Version, Release, Arch string
	Epoch                        int64 // -1: none
	Summary, Description         string
	License, URL                 string
	Requires                     []string
	Files                        map[string][]byte // absolute path: content; mode 0755 under a bin/ folder
	Changelog                    []changelogEntry  // newest first
	BuildTime                    time.Time
}

type changelogEntry struct {
	Time       time.Time
	Name, Text string
}

// writeRPM builds the package of s.
func writeRPM(s buildSpec) ([]byte, error) {
	paths := make([]string, 0, len(s.Files))
	for p := range s.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)

	// The payload: a cpio (newc) archive of ./<path> entries, gzip-compressed.
	var cpio bytes.Buffer
	mtime := uint32(s.BuildTime.Unix())
	modes := make([]uint16, len(paths))
	sizes := make([]uint32, len(paths))
	digests := make([]string, len(paths))
	var installed uint64
	for i, p := range paths {
		data := s.Files[p]
		mode := uint16(0o100644)
		if strings.Contains(p, "bin/") {
			mode = 0o100755
		}
		modes[i], sizes[i] = mode, uint32(len(data))
		sum := sha256.Sum256(data)
		digests[i] = hex.EncodeToString(sum[:])
		installed += uint64(len(data))
		writeCpio(&cpio, "."+p, uint32(i+1), uint32(mode), mtime, data)
	}
	writeCpio(&cpio, "TRAILER!!!", 0, 0, 0, nil)
	var payload bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&payload, gzip.BestCompression)
	zw.Write(cpio.Bytes())
	zw.Close()
	pdigest := sha256.Sum256(payload.Bytes())

	var dirs []string
	dirIdx := make([]uint32, len(paths))
	bases := make([]string, len(paths))
	for i, p := range paths {
		d, b := path.Dir(p)+"/", path.Base(p)
		j := slices.Index(dirs, d)
		if j < 0 {
			dirs, j = append(dirs, d), len(dirs)
		}
		dirIdx[i], bases[i] = uint32(j), b
	}
	n := len(paths)
	strsN := func(v string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	zeros32 := make([]uint32, n)
	zeros16 := make([]uint16, n)
	mtimes := make([]uint32, n)
	inodes := make([]uint32, n)
	for i := range n {
		mtimes[i], inodes[i] = mtime, uint32(i+1)
	}
	full := s.Version + "-" + s.Release
	if s.Epoch >= 0 {
		full = fmt.Sprint(s.Epoch) + ":" + full
	}
	reqNames := []string{"rpmlib(CompressedFileNames)", "rpmlib(FileDigests)", "rpmlib(PayloadFilesHavePrefix)"}
	reqVers := []string{"3.0.4-1", "4.6.0-1", "4.0-1"}
	reqFlags := []uint32{senseLess | senseEqual | senseRpmlib, senseLess | senseEqual | senseRpmlib, senseLess | senseEqual | senseRpmlib}
	for _, r := range s.Requires {
		reqNames, reqVers, reqFlags = append(reqNames, r), append(reqVers, ""), append(reqFlags, 0)
	}

	h := &header{}
	h.set(strsEntry(tagHeaderI18NTable, []string{"C"}))
	h.set(strEntry(tagName, s.Name))
	h.set(strEntry(tagVersion, s.Version))
	h.set(strEntry(tagRelease, s.Release))
	if s.Epoch >= 0 {
		h.set(int32Entry(tagEpoch, uint32(s.Epoch)))
	}
	h.set(i18nEntry(tagSummary, s.Summary))
	h.set(i18nEntry(tagDescription, s.Description))
	h.set(int32Entry(tagBuildTime, mtime))
	h.set(strEntry(tagBuildHost, "telesphorepo"))
	h.set(int32Entry(tagSize, uint32(installed)))
	h.set(strEntry(tagLicense, s.License))
	h.set(i18nEntry(tagGroup, "Unspecified"))
	if s.URL != "" {
		h.set(strEntry(tagURL, s.URL))
	}
	h.set(strEntry(tagOS, "linux"))
	h.set(strEntry(tagArch, s.Arch))
	if n > 0 {
		h.set(int32Entry(tagFileSizes, sizes...))
		h.set(int16Entry(tagFileModes, modes...))
		h.set(int16Entry(tagFileRdevs, zeros16...))
		h.set(int32Entry(tagFileMtimes, mtimes...))
		h.set(strsEntry(tagFileDigests, digests))
		h.set(strsEntry(tagFileLinkTos, strsN("")))
		h.set(int32Entry(tagFileFlags, zeros32...))
		h.set(strsEntry(tagFileUserName, strsN("root")))
		h.set(strsEntry(tagFileGroupName, strsN("root")))
		h.set(int32Entry(tagFileDevices, strsOnes(n)...))
		h.set(int32Entry(tagFileInodes, inodes...))
		h.set(strsEntry(tagFileLangs, strsN("")))
		h.set(int32Entry(tagDirIndexes, dirIdx...))
		h.set(strsEntry(tagBaseNames, bases))
		h.set(strsEntry(tagDirNames, dirs))
		h.set(int32Entry(tagFileDigestAlgo, hashSHA256))
	}
	h.set(strEntry(tagSourceRPM, s.Name+"-"+s.Version+"-"+s.Release+".src.rpm"))
	h.set(strsEntry(tagProvideName, []string{s.Name, s.Name + "(" + s.Arch + ")"}))
	h.set(int32Entry(1112, senseEqual, senseEqual))
	h.set(strsEntry(tagProvideVersion, []string{full, full}))
	h.set(int32Entry(tagRequireFlags, reqFlags...))
	h.set(strsEntry(tagRequireName, reqNames))
	h.set(strsEntry(tagRequireVersion, reqVers))
	if len(s.Changelog) > 0 {
		var ts []uint32
		var ns, xs []string
		for _, c := range s.Changelog {
			ts, ns, xs = append(ts, uint32(c.Time.Unix())), append(ns, c.Name), append(xs, c.Text)
		}
		h.set(int32Entry(tagChangelogTime, ts...))
		h.set(strsEntry(tagChangelogName, ns))
		h.set(strsEntry(tagChangelogText, xs))
	}
	h.set(strEntry(tagPayloadFormat, "cpio"))
	h.set(strEntry(tagPayloadCompressor, "gzip"))
	h.set(strEntry(tagPayloadFlags, "9"))
	h.set(strsEntry(tagPayloadDigest, []string{hex.EncodeToString(pdigest[:])}))
	h.set(int32Entry(tagPayloadDigestAlgo, hashSHA256))
	hdr := h.encode(tagHeaderImmutable)

	hsum := sha256.Sum256(hdr)
	sig := &header{}
	sig.set(strEntry(sigTagSHA256, hex.EncodeToString(hsum[:])))
	sig.set(int32Entry(sigTagSize, uint32(len(hdr)+payload.Len())))
	sig.set(int32Entry(sigTagPayloadSize, uint32(cpio.Len())))
	sigb := sig.encode(tagHeaderSignatures)

	var out bytes.Buffer
	out.Write(lead(s.Name + "-" + full))
	out.Write(sigb)
	out.Write(make([]byte, pad8(len(sigb))))
	out.Write(hdr)
	out.Write(payload.Bytes())
	return out.Bytes(), nil
}

func strsOnes(n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

// lead is the 96-byte lead of a binary package.
func lead(nevr string) []byte {
	b := make([]byte, leadSize)
	copy(b, leadMagic)
	b[4], b[5] = 3, 0                     // format 3.0
	binary.BigEndian.PutUint16(b[6:], 0)  // binary
	binary.BigEndian.PutUint16(b[8:], 1)  // arch number (unused by rpm)
	copy(b[10:75], nevr)                  // name, 66 bytes with its NUL
	binary.BigEndian.PutUint16(b[76:], 1) // os number: Linux
	binary.BigEndian.PutUint16(b[78:], 5) // signature type: header-style
	return b
}

// writeCpio writes one newc entry.
func writeCpio(w *bytes.Buffer, name string, ino, mode, mtime uint32, data []byte) {
	nlink := 1
	if name == "TRAILER!!!" {
		nlink = 1
	}
	fmt.Fprintf(w, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		ino, mode, 0, 0, nlink, mtime, len(data), 0, 0, 0, 0, len(name)+1, 0)
	w.WriteString(name)
	w.WriteByte(0)
	for w.Len()%4 != 0 {
		w.WriteByte(0)
	}
	w.Write(data)
	for w.Len()%4 != 0 {
		w.WriteByte(0)
	}
}
