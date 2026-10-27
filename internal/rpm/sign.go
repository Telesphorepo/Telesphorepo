package rpm

import (
	"bytes"
	"encoding/base64"
	"fmt"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// signatureEntries are the signature header entries of an OpenPGP signature packet sig, as
// rpmsign writes them (sign/rpmgensig.cc, putSignature): a header-only signature in RSAHEADER
// for an RSA key and in DSAHEADER for DSA, ECDSA and EdDSA keys; an rpm 6 package also gets the
// base64 packet in OPENPGP. rpm 4.16 and 4.14 read only the first, so an rpm 6 package keeps it
// too, as rpmsign --rpmv4 does.
func signatureEntries(format int, sig []byte) ([]entry, error) {
	e, err := legacyEntry(sig)
	if err != nil {
		return nil, err
	}
	if format >= 6 {
		return []entry{e, strsEntry(sigTagOpenPGP, []string{base64.StdEncoding.EncodeToString(sig)})}, nil
	}
	return []entry{e}, nil
}

// legacyEntry is the header-only signature entry of sig by its key's algorithm.
func legacyEntry(sig []byte) (entry, error) {
	p, err := packet.Read(bytes.NewReader(sig))
	if err != nil {
		return entry{}, fmt.Errorf("read the signature: %w", err)
	}
	s, ok := p.(*packet.Signature)
	if !ok {
		return entry{}, fmt.Errorf("the signature is a %T", p)
	}
	tag := uint32(sigTagDSA)
	if s.PubKeyAlgo == packet.PubKeyAlgoRSA || s.PubKeyAlgo == packet.PubKeyAlgoRSASignOnly {
		tag = sigTagRSA
	}
	return binEntry(tag, sig), nil
}

// signedSigHeader returns the signature header of p with sig (nil: none) in place of every
// signature it carried, encoded and padded to 8 bytes. The other entries (the digests and the
// sizes) stay: the header and the payload do not change.
func signedSigHeader(sigh *header, format int, sig []byte) ([]byte, error) {
	h := &header{entries: append([]entry(nil), sigh.entries...)}
	h.del(signatureTags...)
	if sig != nil {
		es, err := signatureEntries(format, sig)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			h.set(e)
		}
	}
	b := h.encode(tagHeaderSignatures)
	return append(b, make([]byte, pad8(len(b)))...), nil
}

// signedBy reports whether the signature header sigh holds a header signature by the key keyID.
func signedBy(sigh *header, keyID uint64) bool {
	for _, tag := range []uint32{sigTagRSA, sigTagDSA} {
		e, ok := sigh.get(tag)
		if !ok {
			continue
		}
		p, err := packet.Read(bytes.NewReader(e.Data))
		if s, isSig := p.(*packet.Signature); err == nil && isSig && s.IssuerKeyId != nil && *s.IssuerKeyId == keyID {
			return true
		}
	}
	return false
}
