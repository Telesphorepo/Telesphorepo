// Package token holds the format of the bearer secrets that the server issues: read tokens,
// publish session tokens and any token kind a module adds. A token is
// "<prefix>_<public id>_<secret>": the public id is 12 lower-case hex digits and finds the row,
// the secret is 43 base64url characters (256 random bits) and only its SHA-256 is stored.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

// Prefix names a token kind, e.g. "trp" for a read token.
type Prefix string

// ReadToken is the prefix of a read token (kernel table api_tokens).
const ReadToken Prefix = "trp"

const (
	publicIDLen = 12 // hex digits: 48 bits
	secretLen   = 43 // base64url of 32 bytes
)

var prefixRE = regexp.MustCompile(`^[a-z]{2,8}$`)

// Valid reports whether p has the form of a prefix: 2 to 8 lower-case letters.
func (p Prefix) Valid() bool { return prefixRE.MatchString(string(p)) }

// Mint returns a new token of kind p and its two parts.
func Mint(p Prefix) (publicID, secret, tok string) {
	id := make([]byte, publicIDLen/2)
	sec := make([]byte, 32)
	rand.Read(id) // crypto/rand never fails (Go 1.24+)
	rand.Read(sec)
	publicID = hex.EncodeToString(id)
	secret = base64.RawURLEncoding.EncodeToString(sec)
	return publicID, secret, string(p) + "_" + publicID + "_" + secret
}

// Parse splits tok into its prefix, public id and secret. ok is false when tok does not have the
// form of a token.
func Parse(tok string) (p Prefix, publicID, secret string, ok bool) {
	pre, rest, found := strings.Cut(tok, "_")
	if !found || !Prefix(pre).Valid() || len(rest) != publicIDLen+1+secretLen || rest[publicIDLen] != '_' {
		return "", "", "", false
	}
	publicID, secret = rest[:publicIDLen], rest[publicIDLen+1:]
	for i := 0; i < len(publicID); i++ {
		if c := publicID[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", "", "", false
		}
	}
	if b, err := base64.RawURLEncoding.DecodeString(secret); err != nil || len(b) != 32 {
		return "", "", "", false
	}
	return Prefix(pre), publicID, secret, true
}

// Hash returns the stored form of a secret: its SHA-256.
func Hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// Check reports whether secret matches the stored hash, in constant time.
func Check(hash []byte, secret string) bool {
	return subtle.ConstantTimeCompare(Hash(secret), hash) == 1
}
