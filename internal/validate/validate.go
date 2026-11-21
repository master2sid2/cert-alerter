// Package validate provides field validation helpers and an error type
// that maps field names to machine readable error codes.
package validate

import (
	"fmt"
	"maps"
	"net/mail"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Error codes understood by the web UI.
const (
	Required     = "required"
	Invalid      = "invalid"
	TooLong      = "too_long"
	OutOfRange   = "out_of_range"
	InvalidEmail = "invalid_email"
	Duplicate    = "duplicate"
)

// Errors maps field names to error codes. It implements error.
type Errors map[string]string

// Add records the first error for a field.
func (e Errors) Add(field, code string) {
	if _, ok := e[field]; !ok {
		e[field] = code
	}
}

// Err returns nil when there are no errors.
func (e Errors) Err() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

func (e Errors) Error() string {
	var b strings.Builder
	b.WriteString("validation failed:")
	for _, k := range slices.Sorted(maps.Keys(e)) {
		fmt.Fprintf(&b, " %s=%s", k, e[k])
	}
	return b.String()
}

// Email normalises and validates a bare e-mail address.
func Email(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" {
		return "", false
	}
	local, domain, ok := strings.Cut(addr.Address, "@")
	if !ok || local == "" || domain == "" || strings.ContainsAny(domain, " []") {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

// Text trims s and checks that it is at most maxLen runes long and contains
// no control characters (newlines allowed when multiline is true).
func Text(e Errors, field, s string, maxLen int, required, multiline bool) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "" && required:
		e.Add(field, Required)
	case utf8.RuneCountInString(s) > maxLen:
		e.Add(field, TooLong)
	case hasControl(s, multiline):
		e.Add(field, Invalid)
	}
	return s
}

// Range validates that v is within [lo, hi].
func Range(e Errors, field string, v, lo, hi int) {
	if v < lo || v > hi {
		e.Add(field, OutOfRange)
	}
}

func hasControl(s string, multiline bool) bool {
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
