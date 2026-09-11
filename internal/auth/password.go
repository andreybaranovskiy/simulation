// Package auth handles credential verification, server-side sessions and the
// per-project permission checks the API enforces.
//
// Everything here is behind the Provider interface so a Windows Authentication
// backend can be added later without touching handlers.
package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is deliberately length-based rather than a composition
// rule: length is what actually resists offline cracking.
const MinPasswordLength = 10

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrAccountDisabled    = errors.New("account is disabled")
	ErrWeakPassword       = errors.New("password is too weak")
)

// HashPassword produces a bcrypt hash at the configured cost.
func HashPassword(plain string, cost int) (string, error) {
	if err := ValidatePassword(plain); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword verifies plain against a stored hash. It returns
// ErrInvalidCredentials for a mismatch so callers cannot accidentally
// distinguish "wrong password" from "no such user" in a response.
func CheckPassword(hash, plain string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("compare password: %w", err)
	}
	return nil
}

// NeedsRehash reports whether a stored hash was made at a lower cost than the
// current setting, so a successful login can transparently upgrade it.
func NeedsRehash(hash string, cost int) bool {
	current, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		return true
	}
	return current < cost
}

// ValidatePassword enforces the minimum policy: long enough, not entirely one
// character class, and not a well-known weak string.
func ValidatePassword(plain string) error {
	if len([]rune(plain)) < MinPasswordLength {
		return fmt.Errorf("%w: use at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	// bcrypt silently truncates past 72 bytes, which would make the tail of a
	// long passphrase meaningless. Reject rather than mislead.
	if len(plain) > 72 {
		return fmt.Errorf("%w: use at most 72 bytes", ErrWeakPassword)
	}
	if isSingleCharacterClass(plain) {
		return fmt.Errorf("%w: mix letters with digits or symbols", ErrWeakPassword)
	}
	if commonPasswords[strings.ToLower(plain)] {
		return fmt.Errorf("%w: that password is too common", ErrWeakPassword)
	}
	return nil
}

func isSingleCharacterClass(s string) bool {
	var hasLetter, hasDigit, hasOther bool
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasOther = true
		}
	}
	classes := 0
	for _, ok := range []bool{hasLetter, hasDigit, hasOther} {
		if ok {
			classes++
		}
	}
	return classes < 2
}

// commonPasswords is a short denylist of strings that survive the length and
// class checks but are still among the first guesses any attacker makes.
var commonPasswords = map[string]bool{
	"password1":      true,
	"password12":     true,
	"password123":    true,
	"password1234":   true,
	"passw0rd123":    true,
	"qwerty12345":    true,
	"qwerty123456":   true,
	"letmein1234":    true,
	"welcome1234":    true,
	"admin12345":     true,
	"administrator1": true,
	"iloveyou123":    true,
	"1q2w3e4r5t":     true,
	"trustno1234":    true,
	"changeme123":    true,
	"simulation1":    true,
}

// NormalizeEmail lower-cases and trims an address for the uniqueness index.
// The original spelling is kept on the user row for display.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
