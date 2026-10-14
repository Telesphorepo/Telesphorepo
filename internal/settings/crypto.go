package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
)

const hkdfInfo = "telesphorepo settings v1"

// loadMasterKey returns the 32-byte master key from TR_MASTER_KEY, else from
// <DataDir>/master.key, generating that file (mode 0600) on first start.
func loadMasterKey(boot *config.Boot, log *slog.Logger) ([]byte, error) {
	if boot.MasterKey != "" {
		k, err := decodeKey(boot.MasterKey)
		if err != nil {
			return nil, fmt.Errorf("TR_MASTER_KEY: %w", err)
		}
		return k, nil
	}
	file := config.JoinDataDir(boot.DataDir, config.MasterKeyFileName)
	b, err := os.ReadFile(file)
	if err == nil {
		k, err := decodeKey(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	k := make([]byte, 32)
	rand.Read(k)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(k) + "\n"); err != nil {
		f.Close()
		os.Remove(file)
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(file)
		return nil, fmt.Errorf("write master key: %w", err)
	}
	log.Info("generated master key for secret settings; back it up or move it to TR_MASTER_KEY", "file", file)
	return k, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil {
			if len(k) != 32 {
				return nil, fmt.Errorf("must be 32 bytes, got %d (generate one with: openssl rand -base64 32)", len(k))
			}
			return k, nil
		}
	}
	return nil, errors.New("not valid base64 (generate one with: openssl rand -base64 32)")
}

func newAEAD(master []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, master, nil, hkdfInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts plaintext for setting key: base64(nonce || ciphertext), AAD = key.
func seal(aead cipher.AEAD, key string, plaintext []byte) string {
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plaintext, []byte(key)))
}

func open(aead cipher.AEAD, key, sealed string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(b) < aead.NonceSize() {
		return nil, errors.New("malformed ciphertext")
	}
	n := aead.NonceSize()
	pt, err := aead.Open(nil, b[:n], b[n:], []byte(key))
	if err != nil {
		return nil, errors.New("cannot decrypt (was the master key changed?)")
	}
	return pt, nil
}
