package auth

import (
	"errors"
	"strings"
	"testing"
)

// testCost keeps bcrypt cheap in tests. Production uses the configured cost.
const testCost = 10

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"good passphrase", "correct horse battery 7", false},
		{"letters and digits", "sim2026platform", false},
		{"too short", "short1!", true},
		{"letters only", "abcdefghijklmnop", true},
		{"digits only", "12345678901234", true},
		{"common", "password123", true},
		{"common, different case", "Password123", true},
		{"past bcrypt 72-byte limit", strings.Repeat("a1", 40), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidatePassword(%q) = nil, want an error", tt.password)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidatePassword(%q) = %v, want nil", tt.password, err)
			}
			if tt.wantErr && !errors.Is(err, ErrWeakPassword) {
				t.Fatalf("error %v does not wrap ErrWeakPassword", err)
			}
		})
	}
}

func TestHashAndCheckPassword(t *testing.T) {
	const plain = "terminal throughput 42"

	hash, err := HashPassword(plain, testCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == plain || !strings.HasPrefix(hash, "$2a$") {
		t.Fatalf("hash %q does not look like bcrypt output", hash)
	}

	if err := CheckPassword(hash, plain); err != nil {
		t.Fatalf("CheckPassword with the right password: %v", err)
	}

	err = CheckPassword(hash, "terminal throughput 43")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("CheckPassword with a wrong password = %v, want ErrInvalidCredentials", err)
	}
}

// Two hashes of the same password must differ, or the salt is not doing its job.
func TestHashPasswordIsSalted(t *testing.T) {
	const plain = "yard congestion 91"

	first, err := HashPassword(plain, testCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(plain, testCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if first == second {
		t.Fatal("two hashes of the same password are identical, so the salt is not random")
	}
}

func TestNeedsRehash(t *testing.T) {
	hash, err := HashPassword("crane cycle time 12", testCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if NeedsRehash(hash, testCost) {
		t.Error("a hash at the current cost should not need rehashing")
	}
	if !NeedsRehash(hash, testCost+1) {
		t.Error("a hash below the current cost should need rehashing")
	}
	if !NeedsRehash("not a bcrypt hash", testCost) {
		t.Error("an unreadable hash should be treated as needing a rehash")
	}
}

// The dummy hash exists to equalise login timing for unknown accounts. If it
// ever stopped being a valid bcrypt hash, that defence would silently vanish.
func TestDummyHashIsValidBcrypt(t *testing.T) {
	err := CheckPassword(dummyHash, "anything at all")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("comparing against dummyHash = %v, want ErrInvalidCredentials", err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := map[string]string{
		"  User@Example.COM ": "user@example.com",
		"already@lower.dev":   "already@lower.dev",
	}
	for in, want := range tests {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
