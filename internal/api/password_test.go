package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestHashPasswordProducesBcrypt(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword() returned error: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("hash %q is not in bcrypt format", hash)
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	const pw = "correct horse battery staple"

	first, err := hashPassword(pw)
	if err != nil {
		t.Fatalf("hashPassword() returned error: %v", err)
	}
	second, err := hashPassword(pw)
	if err != nil {
		t.Fatalf("hashPassword() returned error: %v", err)
	}

	if first == second {
		t.Error("hashing the same password twice produced identical output; " +
			"the hash is not salted and is vulnerable to rainbow tables")
	}

	// Both must still verify.
	for i, h := range []string{first, second} {
		if ok, _ := verifyPassword(h, pw); !ok {
			t.Errorf("hash %d did not verify against its own password", i)
		}
	}
}

func TestVerifyPassword(t *testing.T) {
	const pw = "correct horse battery staple"
	hash, err := hashPassword(pw)
	if err != nil {
		t.Fatalf("hashPassword() returned error: %v", err)
	}

	tests := []struct {
		name        string
		stored      string
		supplied    string
		wantOK      bool
		wantUpgrade bool
	}{
		{"correct password", hash, pw, true, false},
		{"wrong password", hash, "wrong", false, false},
		{"empty supplied", hash, "", false, false},
		{"empty stored", "", pw, false, false},
		{"stored is garbage", "not-a-hash", pw, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, upgrade := verifyPassword(tt.stored, tt.supplied)
			if ok != tt.wantOK {
				t.Errorf("verifyPassword() ok = %v, want %v", ok, tt.wantOK)
			}
			if upgrade != tt.wantUpgrade {
				t.Errorf("verifyPassword() needsUpgrade = %v, want %v", upgrade, tt.wantUpgrade)
			}
		})
	}
}

// TestVerifyPasswordAcceptsLegacyHash covers the migration path for
// databases written before bcrypt was adopted.
func TestVerifyPasswordAcceptsLegacyHash(t *testing.T) {
	const pw = "legacy-password"
	digest := sha256.Sum256([]byte(pw))
	legacy := hex.EncodeToString(digest[:])

	ok, needsUpgrade := verifyPassword(legacy, pw)
	if !ok {
		t.Fatal("legacy SHA-256 hash did not verify; existing installs would be locked out")
	}
	if !needsUpgrade {
		t.Error("legacy hash did not request an upgrade; it would never migrate to bcrypt")
	}

	if ok, _ := verifyPassword(legacy, "wrong"); ok {
		t.Error("legacy verification accepted the wrong password")
	}
}

// TestPasswordLongerThan72BytesIsNotTruncated guards the bcrypt input limit.
// Without pre-hashing, bcrypt ignores everything past 72 bytes, so two
// passphrases sharing a long prefix would be interchangeable.
func TestPasswordLongerThan72BytesIsNotTruncated(t *testing.T) {
	prefix := strings.Repeat("a", 72)
	first := prefix + "ONE"
	second := prefix + "TWO"

	hash, err := hashPassword(first)
	if err != nil {
		t.Fatalf("hashPassword() returned error: %v", err)
	}

	if ok, _ := verifyPassword(hash, first); !ok {
		t.Error("long password did not verify against its own hash")
	}
	if ok, _ := verifyPassword(hash, second); ok {
		t.Error("a password differing only past byte 72 was accepted; " +
			"bcrypt truncation is not being handled")
	}
}

func TestIsLegacySHA256Hash(t *testing.T) {
	bcryptHash, err := hashPassword("x")
	if err != nil {
		t.Fatalf("hashPassword() returned error: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid sha256 hex", strings.Repeat("ab", 32), true},
		{"bcrypt hash", bcryptHash, false},
		{"too short", strings.Repeat("ab", 16), false},
		{"right length but not hex", strings.Repeat("zz", 32), false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLegacySHA256Hash(tt.input); got != tt.want {
				t.Errorf("isLegacySHA256Hash(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
