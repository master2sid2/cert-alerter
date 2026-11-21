// Package auth contains password hashing, password policies and helpers
// for random tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters following the OWASP recommendation (19 MiB, t=2, p=1).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// MaxPasswordLength bounds the work done for a single hash.
const MaxPasswordLength = 128

var errInvalidHash = errors.New("invalid password hash")

// HashPassword derives an Argon2id hash in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a PHC encoded Argon2id hash in
// constant time.
func VerifyPassword(encoded, password string) bool {
	ok, _ := verify(encoded, password)
	return ok
}

func verify(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errInvalidHash
	}
	var (
		memory  uint32
		time    uint32
		threads uint8
	)
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errInvalidHash
	}
	if memory == 0 || memory > 1<<20 || time == 0 || time > 16 || threads == 0 {
		return false, errInvalidHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errInvalidHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false, errInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is used to spend the same time on unknown accounts as on
// known ones, so login timing does not reveal which e-mails exist.
var dummyHash, _ = HashPassword("timing-equalizer-not-a-real-password")

// DummyVerify performs a throw-away verification.
func DummyVerify(password string) { _ = VerifyPassword(dummyHash, password) }

// NewToken returns a random URL-safe token and its SHA-256 hex digest.
// Only the digest is stored; the token is handed to the user once.
func NewToken() (token, hash string) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token)
}

// HashToken returns the SHA-256 hex digest of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// PasswordPolicy describes password requirements configured by admins.
type PasswordPolicy struct {
	MinLength      int  `json:"min_length"`
	RequireUpper   bool `json:"require_upper"`
	RequireLower   bool `json:"require_lower"`
	RequireDigit   bool `json:"require_digit"`
	RequireSpecial bool `json:"require_special"`
	// HistoryCount forbids reusing the last N passwords (0 = no check).
	HistoryCount int `json:"history_count"`
	// MaxAgeDays forces a password change after N days (0 = never).
	MaxAgeDays int `json:"max_age_days"`
}

// Policy violation codes (localised by the web UI).
const (
	ViolationMinLength     = "min_length"
	ViolationMaxLength     = "max_length"
	ViolationUpper         = "upper"
	ViolationLower         = "lower"
	ViolationDigit         = "digit"
	ViolationSpecial       = "special"
	ViolationContainsEmail = "contains_email"
	ViolationReused        = "reused"
)

// DefaultPasswordPolicy returns the policy used until an admin changes it.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{
		MinLength:    10,
		RequireUpper: true,
		RequireLower: true,
		RequireDigit: true,
		HistoryCount: 3,
	}
}

// Check returns the list of violated rules (empty when the password is
// acceptable). The e-mail is used to reject passwords containing the
// account name.
func (p PasswordPolicy) Check(password, email string) []string {
	var v []string
	n := utf8.RuneCountInString(password)
	if n < max(p.MinLength, 1) {
		v = append(v, ViolationMinLength)
	}
	if n > MaxPasswordLength {
		v = append(v, ViolationMaxLength)
	}
	var upper, lower, digit, special bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		case !unicode.IsLetter(r):
			special = true
		}
	}
	if p.RequireUpper && !upper {
		v = append(v, ViolationUpper)
	}
	if p.RequireLower && !lower {
		v = append(v, ViolationLower)
	}
	if p.RequireDigit && !digit {
		v = append(v, ViolationDigit)
	}
	if p.RequireSpecial && !special {
		v = append(v, ViolationSpecial)
	}
	if local, _, ok := strings.Cut(strings.ToLower(email), "@"); ok && utf8.RuneCountInString(local) >= 4 &&
		strings.Contains(strings.ToLower(password), local) {
		v = append(v, ViolationContainsEmail)
	}
	return v
}

// Reused reports whether password matches one of the given hashes.
func Reused(password string, hashes []string) bool {
	for _, h := range hashes {
		if VerifyPassword(h, password) {
			return true
		}
	}
	return false
}
