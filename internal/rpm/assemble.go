package rpm

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"io"
)

// assemble writes the stored file of a package: the lead of src, the signature header sigh
// (already padded), then src from the header on. It checks the payload against the header's
// payload digest as the bytes pass, so the payload is read once; a mismatch is a badPkg. It
// returns where the header starts in the written file.
func assemble(dst io.Writer, src io.ReaderAt, size int64, p *pkgInfo, sigh []byte) (int64, error) {
	lead := make([]byte, leadSize)
	if _, err := src.ReadAt(lead, 0); err != nil {
		return 0, err
	}
	if _, err := dst.Write(lead); err != nil {
		return 0, err
	}
	if _, err := dst.Write(sigh); err != nil {
		return 0, err
	}
	hdrEnd := p.headerStart + p.headerLen
	if _, err := io.Copy(dst, io.NewSectionReader(src, p.headerStart, p.headerLen)); err != nil {
		return 0, err
	}
	var d hash.Hash = sha256.New()
	if p.payloadAlgo == hashSHA512 {
		d = sha512.New()
	}
	if _, err := io.Copy(io.MultiWriter(dst, d), io.NewSectionReader(src, hdrEnd, size-hdrEnd)); err != nil {
		return 0, err
	}
	if hex.EncodeToString(d.Sum(nil)) != p.payloadDigest {
		return 0, badPkg("the payload does not match the header's payload digest: the package is damaged")
	}
	return int64(leadSize + len(sigh)), nil
}
