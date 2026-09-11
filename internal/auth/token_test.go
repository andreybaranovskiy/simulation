package auth

import "testing"

func TestNewSessionTokenIsUniqueAndHashed(t *testing.T) {
	tokenA, idA, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	tokenB, idB, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}

	if tokenA == tokenB {
		t.Fatal("two session tokens are identical")
	}
	if idA == idB {
		t.Fatal("two session ids are identical")
	}

	// The stored id must be the hash, never the token itself: that is the
	// whole point of storing a digest.
	if idA == tokenA {
		t.Fatal("the session id equals the raw token")
	}
	if got := HashToken(tokenA); got != idA {
		t.Fatalf("HashToken(token) = %q, want the returned id %q", got, idA)
	}
	if len(idA) != 64 {
		t.Fatalf("session id is %d characters, want 64 hex characters", len(idA))
	}
}

func TestHashTokenIsStable(t *testing.T) {
	const token = "a-fixed-token-value"
	if HashToken(token) != HashToken(token) {
		t.Fatal("HashToken is not deterministic")
	}
	if HashToken(token) == HashToken(token+"x") {
		t.Fatal("different tokens hash to the same value")
	}
}

func TestNewRandomHex(t *testing.T) {
	a, err := NewRandomHex(16)
	if err != nil {
		t.Fatalf("NewRandomHex: %v", err)
	}
	b, err := NewRandomHex(16)
	if err != nil {
		t.Fatalf("NewRandomHex: %v", err)
	}

	if len(a) != 32 {
		t.Fatalf("NewRandomHex(16) returned %d characters, want 32", len(a))
	}
	if a == b {
		t.Fatal("two random values are identical")
	}
}
