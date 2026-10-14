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

// Load reads an armored private key (first entity with a signing-capable key). If
// passphraseFile is non-empty, its trimmed contents decrypt the key.
func Load(keyFile, passphraseFile string) (*Signer, error) {
	f, err := os.Open(keyFile)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	defer f.Close()
	ring, err := openpgp.ReadArmoredKeyRing(f)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	var pass []byte
	if passphraseFile != "" {
		b, err := os.ReadFile(passphraseFile)
		if err != nil {
			return nil, fmt.Errorf("signing passphrase: %w", err)
		}
		pass = []byte(strings.TrimSpace(string(b)))
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
	return nil, errors.New("signing key: no entity with a usable signing key")
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
