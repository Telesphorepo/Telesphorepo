// Package signing holds the repository signing keys and wraps ProtonMail go-crypto
// for repository index signing.
package signing

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"encoding"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

type Signer struct {
	entity *openpgp.Entity
	cfg    *packet.Config
	pub    []byte
}

// Parse loads an armored private key block (first entity with a signing-capable key),
// decrypting it with pass when it is encrypted.
func Parse(armored, pass []byte) (*Signer, error) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	now := time.Now()
	for _, e := range ring {
		if e.PrivateKey == nil {
			continue
		}
		if _, ok := e.SigningKey(now); !ok {
			continue
		}
		if err := decrypt(e, pass); err != nil {
			return nil, err
		}
		pub, err := armorPublic(e)
		if err != nil {
			return nil, err
		}
		return &Signer{entity: e, cfg: &packet.Config{DefaultHash: crypto.SHA512}, pub: pub}, nil
	}
	return nil, errors.New("signing key: no private key with a usable signing key (expected an armored PGP PRIVATE KEY block)")
}

// Encrypted reports whether the armored private key block needs a passphrase. A block that is
// not a private key reports false.
func Encrypted(armored []byte) bool {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return false
	}
	for _, e := range ring {
		if e.PrivateKey != nil && e.PrivateKey.Encrypted {
			return true
		}
		for _, s := range e.Subkeys {
			if s.PrivateKey != nil && s.PrivateKey.Encrypted {
				return true
			}
		}
	}
	return false
}

func decrypt(e *openpgp.Entity, pass []byte) error {
	unlock := func(k *packet.PrivateKey) error {
		if k == nil || !k.Encrypted {
			return nil
		}
		if len(pass) == 0 {
			return errors.New("signing key is encrypted but no passphrase was provided")
		}
		if err := k.Decrypt(pass); err != nil {
			return fmt.Errorf("decrypt signing key: %w", err)
		}
		return nil
	}
	if err := unlock(e.PrivateKey); err != nil {
		return err
	}
	for _, s := range e.Subkeys {
		if err := unlock(s.PrivateKey); err != nil {
			return err
		}
	}
	return nil
}

func armorPublic(e *openpgp.Entity) ([]byte, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return nil, err
	}
	if err := e.Serialize(w); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ClearSign writes an InRelease-style clearsigned document (SHA-256/512 digest).
func (s *Signer) ClearSign(w io.Writer, data []byte) error {
	wc, err := clearsign.Encode(w, s.entity.PrivateKey, s.cfg)
	if err != nil {
		return err
	}
	if _, err := wc.Write(data); err != nil {
		return err
	}
	return wc.Close()
}

// DetachSign writes an armored detached signature (Release.gpg).
func (s *Signer) DetachSign(w io.Writer, data []byte) error {
	return openpgp.ArmoredDetachSign(w, s.entity, bytes.NewReader(data), s.cfg)
}

// DetachSignBinary writes a binary detached signature of data (pacman's <repo>.db.sig).
func (s *Signer) DetachSignBinary(w io.Writer, data io.Reader) error {
	return openpgp.DetachSign(w, s.entity, data, s.cfg)
}

// ErrNotV4 is a key whose signatures cannot be made from a saved hash state: a version 6
// signature hashes a salt before the data.
var ErrNotV4 = errors.New("the signing key is not an OpenPGP version 4 key")

// SignSHA256State returns a binary detached signature (SHA-256) of the bytes whose SHA-256 state
// is state (the encoding.BinaryMarshaler form of a crypto/sha256 hash after them), so a file is
// signed again without reading it. It needs a version 4 key (ErrNotV4).
func (s *Signer) SignSHA256State(state []byte) ([]byte, error) {
	k, ok := s.entity.SigningKey(time.Now())
	if !ok || k.PrivateKey == nil {
		return nil, errors.New("the signing key has no usable signing key")
	}
	if k.PrivateKey.Version != 4 {
		return nil, ErrNotV4
	}
	h := sha256.New()
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(state); err != nil {
		return nil, fmt.Errorf("hash state: %w", err)
	}
	sig := &packet.Signature{
		Version:           4,
		SigType:           packet.SigTypeBinary,
		PubKeyAlgo:        k.PrivateKey.PubKeyAlgo,
		Hash:              crypto.SHA256,
		CreationTime:      time.Now(),
		IssuerKeyId:       &k.PrivateKey.KeyId,
		IssuerFingerprint: k.PrivateKey.Fingerprint,
	}
	if err := sig.Sign(h, k.PrivateKey, &packet.Config{DefaultHash: crypto.SHA256}); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := sig.Serialize(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PublicKeyArmored returns the armored public key block.
func (s *Signer) PublicKeyArmored() []byte { return s.pub }

// Generate creates a new Ed25519 signing key (no passphrase), returning armored private
// and public key blocks.
func Generate(name, email string) (privateArmored, publicArmored []byte, err error) {
	e, err := openpgp.NewEntity(name, "Telesphorepo repository signing key", email, &packet.Config{
		Algorithm:   packet.PubKeyAlgoEdDSA,
		DefaultHash: crypto.SHA256,
		V6Keys:      false,
		Time:        time.Now,
	})
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := e.SerializePrivate(w, nil); err != nil {
		return nil, nil, err
	}
	if err := w.Close(); err != nil {
		return nil, nil, err
	}
	if publicArmored, err = armorPublic(e); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), publicArmored, nil
}

// Fingerprint returns the signing entity's primary key fingerprint (upper-case hex).
func (s *Signer) Fingerprint() string { return fmt.Sprintf("%X", s.entity.PrimaryKey.Fingerprint) }

// privateArmored returns the armored private key without a passphrase (Parse decrypted it),
// with the key's own signatures.
func (s *Signer) privateArmored() ([]byte, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return nil, err
	}
	if err := s.entity.SerializePrivateWithoutSigning(w, nil); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
