package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the work factor for password hashing.
//
// Canarium's documented deployment target is a Raspberry Pi 3, where this
// costs roughly a second per login — acceptable for an interactive admin
// login, and far beyond the reach of the offline attack that unsalted
// SHA-256 invited.
//
// It is a var rather than a const solely so tests can lower it; see
// TestMain. Nothing in production mutates it.
// productionBcryptCost is the shipped work factor, kept as a constant so a
// test can assert on it even when bcryptCost has been lowered.
const productionBcryptCost = 12

var bcryptCost = productionBcryptCost

// MinPasswordLength is the shortest admin password accepted at setup.
const MinPasswordLength = 12

// hashPassword derives a bcrypt hash suitable for storage.
func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(bcryptPrehash(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(h), nil
}

// verifyPassword reports whether supplied matches the stored hash.
//
// It accepts both the current bcrypt format and the legacy unsalted SHA-256
// hex digest written by earlier versions. needsUpgrade is true when the
// stored hash is in the legacy format and the caller should rewrite it with
// hashPassword; see handleLogin.
//
// The legacy comparison uses a constant-time compare. It is still a bad
// scheme — an attacker with the database can crack it offline with
// commodity hardware — which is precisely why matching one triggers an
// immediate upgrade.
func verifyPassword(stored, supplied string) (ok bool, needsUpgrade bool) {
	if stored == "" {
		return false, false
	}

	if isLegacySHA256Hash(stored) {
		digest := sha256.Sum256([]byte(supplied))
		encoded := hex.EncodeToString(digest[:])
		if subtle.ConstantTimeCompare([]byte(encoded), []byte(stored)) == 1 {
			return true, true
		}
		return false, false
	}

	err := bcrypt.CompareHashAndPassword([]byte(stored), bcryptPrehash(supplied))
	return err == nil, false
}

// bcryptPrehash reduces a password to a fixed-length token before bcrypt.
//
// bcrypt silently truncates input at 72 bytes, so without this a
// sufficiently long passphrase would have its tail ignored — two distinct
// passphrases sharing a 72-byte prefix would be interchangeable. Hashing to
// a SHA-256 digest and base64-encoding it yields 44 bytes with no NUL
// octets (bcrypt also truncates at the first NUL), so the full password
// always contributes.
//
// This is the same construction as passlib's bcrypt_sha256.
func bcryptPrehash(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	encoded := base64.RawStdEncoding.EncodeToString(sum[:])
	return []byte(encoded)
}

// isLegacySHA256Hash reports whether stored looks like the 64-character hex
// SHA-256 digest used before bcrypt was adopted. bcrypt hashes always begin
// with "$2", so the two formats are unambiguous.
func isLegacySHA256Hash(stored string) bool {
	if strings.HasPrefix(stored, "$2") {
		return false
	}
	if len(stored) != hex.EncodedLen(sha256.Size) {
		return false
	}
	_, err := hex.DecodeString(stored)
	return err == nil
}
