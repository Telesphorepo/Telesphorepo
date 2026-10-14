// Package signing wraps ProtonMail go-crypto for Debian index signing (spec §9.3).
package signing

import "io"

type Signer struct{}

// Load reads an armored private key (first entity with a signing-capable key). If
// passphraseFile is non-empty, its trimmed contents decrypt the key.
func Load(keyFile, passphraseFile string) (*Signer, error) { panic("TODO") }

// ClearSign writes an InRelease-style clearsigned document (SHA-256/512 digest).
func (s *Signer) ClearSign(w io.Writer, data []byte) error { panic("TODO") }

// DetachSign writes an armored detached signature (Release.gpg).
func (s *Signer) DetachSign(w io.Writer, data []byte) error { panic("TODO") }

// PublicKeyArmored returns the armored public key block.
func (s *Signer) PublicKeyArmored() []byte { panic("TODO") }

// Generate creates a new Ed25519 signing key (no passphrase), returning armored private
// and public key blocks.
func Generate(name, email string) (privateArmored, publicArmored []byte, err error) {
	panic("TODO")
}
