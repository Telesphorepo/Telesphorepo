// Package signing wraps ProtonMail go-crypto for Debian index signing (spec §9.3).
package signing

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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

// Load reads an armored private key file (see Parse). If passphraseFile is non-empty, its
// trimmed contents decrypt the key.
func Load(keyFile, passphraseFile string) (*Signer, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	pass, err := ReadPassphrase(passphraseFile)
	if err != nil {
		return nil, err
	}
	return Parse(b, pass)
}

// ReadPassphrase returns the trimmed contents of file, or nil when file is empty.
func ReadPassphrase(file string) ([]byte, error) {
	if file == "" {
		return nil, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("signing passphrase: %w", err)
	}
	return []byte(strings.TrimSpace(string(b))), nil
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

// PublicKeyArmored returns the armored public key block.
func (s *Signer) PublicKeyArmored() []byte { return s.pub }

// Generate creates a new Ed25519 signing key (no passphrase), returning armored private
// and public key blocks.
func Generate(name, email string) (privateArmored, publicArmored []byte, err error) {
	e, err := openpgp.NewEntity(name, "Debian repository signing key", email, &packet.Config{
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

// Fingerprint returns the primary key fingerprint (upper-case hex) of the first entity in an
// armored key block (public or private).
func Fingerprint(armored []byte) (string, error) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return "", err
	}
	if len(ring) == 0 {
		return "", errors.New("no key found")
	}
	return fmt.Sprintf("%X", ring[0].PrimaryKey.Fingerprint), nil
}

// GenerateFile writes a new Ed25519 signing key (armored private key, mode 0600) to keyFile,
// refusing to overwrite an existing file, and returns its fingerprint.
func GenerateFile(keyFile, name, email string) (string, error) {
	priv, _, err := Generate(name, email)
	if err != nil {
		return "", fmt.Errorf("generate signing key: %w", err)
	}
	fp, err := Fingerprint(priv)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(priv); err != nil {
		f.Close()
		os.Remove(keyFile)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(keyFile)
		return "", err
	}
	return fp, nil
}
