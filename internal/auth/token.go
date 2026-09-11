package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the entropy in a session token. 32 bytes is well past any
// practical guessing attack and keeps the cookie short.
const tokenBytes = 32

// NewSessionToken returns an opaque token for the cookie and the SHA-256 hex
// digest stored as the session row's primary key. Only the digest reaches the
// database, so a dump of the sessions table cannot be replayed.
func NewSessionToken() (token, id string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken maps a cookie value to its session row ID.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewRandomHex returns n random bytes hex-encoded, used for CSRF tokens and
// one-off identifiers.
func NewRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
