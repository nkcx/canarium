package api

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewSessionTokenIsUniqueAndWellFormed(t *testing.T) {
	const iterations = 10000

	seen := make(map[string]struct{}, iterations)
	for i := 0; i < iterations; i++ {
		token, err := newSessionToken()
		if err != nil {
			t.Fatalf("newSessionToken() returned error: %v", err)
		}

		if got, want := len(token), sessionTokenBytes*2; got != want {
			t.Fatalf("token length = %d, want %d", got, want)
		}
		if _, err := hex.DecodeString(token); err != nil {
			t.Fatalf("token %q is not valid hex: %v", token, err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("duplicate token generated after %d iterations: %q", i, token)
		}
		seen[token] = struct{}{}
	}
}

// TestNewSessionTokenIsNotClockDerived guards against a regression to the
// previous implementation, which built each token byte from
// time.Now().UnixNano(). Tokens generated back to back in a tight loop were
// therefore strongly correlated: successive tokens shared a long common
// prefix because the high-order clock bits barely moved between calls.
//
// Cryptographically random tokens share essentially no prefix.
func TestNewSessionTokenIsNotClockDerived(t *testing.T) {
	const (
		iterations      = 500
		maxSharedPrefix = 4 // hex chars; 16 bits. Chance of exceeding is ~1e-5 per pair.
	)

	prev, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken() returned error: %v", err)
	}

	for i := 0; i < iterations; i++ {
		cur, err := newSessionToken()
		if err != nil {
			t.Fatalf("newSessionToken() returned error: %v", err)
		}

		if n := commonPrefixLen(prev, cur); n > maxSharedPrefix {
			t.Errorf("consecutive tokens share a %d-character prefix (max %d), "+
				"suggesting they are not independently random:\n  %q\n  %q",
				n, maxSharedPrefix, prev, cur)
		}
		prev = cur
	}
}

// TestStoredSessionIsHashed verifies that what lands in the database is a
// digest, not the cookie value itself, so a database leak does not yield
// usable session cookies.
func TestStoredSessionIsHashed(t *testing.T) {
	token, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken() returned error: %v", err)
	}

	stored := hashToken(token)

	if strings.Contains(stored, token) {
		t.Errorf("stored session key embeds the raw token\n  stored: %q\n  token:  %q",
			stored, token)
	}
	if stored == hashToken(token+"x") {
		t.Error("hashToken collides for different tokens")
	}
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
