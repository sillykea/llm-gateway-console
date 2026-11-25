// Package auth handles console users: password hashes, the users file,
// signed session cookies and a limit on failed logins.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PBKDF2-HMAC-SHA256 with 600,000 iterations is the OWASP recommendation for
// this algorithm. It's in the standard library (Go 1.24+), so there's no
// dependency; see docs/adr/0001.
const (
	iterations = 600_000
	saltLen    = 16
	keyLen     = 32
	scheme     = "pbkdf2-sha256"
)

var b64 = base64.RawStdEncoding

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<hash>".
func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("password must be at least 12 characters")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", scheme, iterations, b64.EncodeToString(salt), b64.EncodeToString(dk)), nil
}

// VerifyPassword checks password against an encoded hash in constant time.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 100_000 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when a username doesn't exist, so a login
// for a missing user takes as long as one with a wrong password and the
// response time doesn't reveal which usernames exist.
var dummyHash = func() string {
	h, err := HashPassword("not-a-real-password")
	if err != nil {
		panic(err)
	}
	return h
}()
