package auth_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/master2sid2/cert-alerter/internal/auth"
)

func TestHashAndVerify(t *testing.T) {
	h, err := auth.HashPassword("Correct horse 1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected hash format %q", h)
	}
	if !auth.VerifyPassword(h, "Correct horse 1") || auth.VerifyPassword(h, "correct horse 1") {
		t.Fatal("verification mismatch")
	}
	h2, _ := auth.HashPassword("Correct horse 1")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=0,t=0,p=0$x$y", "$bcrypt$..."} {
		if auth.VerifyPassword(bad, "x") {
			t.Fatalf("invalid hash %q verified", bad)
		}
	}
	if !auth.Reused("pw", []string{h2, mustHash(t, "pw")}) || auth.Reused("other", []string{h2}) {
		t.Fatal("reuse detection")
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPolicy(t *testing.T) {
	p := auth.PasswordPolicy{MinLength: 10, RequireUpper: true, RequireLower: true, RequireDigit: true, RequireSpecial: true}
	tests := []struct {
		pw, email string
		want      []string
	}{
		{"Aa1!aaaaaa", "", nil},
		{"Κωδικός-2026ω", "", nil}, // non-Latin letters count as upper/lower case
		{"short", "", []string{auth.ViolationMinLength, auth.ViolationUpper, auth.ViolationDigit, auth.ViolationSpecial}},
		{"alllowercase1!", "", []string{auth.ViolationUpper}},
		{"Ivanov-2026!x", "ivanov@example.com", []string{auth.ViolationContainsEmail}},
		{"Ivan-2026!xx", "ivan@example.com", []string{auth.ViolationContainsEmail}},
		{"Ab1!" + strings.Repeat("x", 130), "", []string{auth.ViolationMaxLength}},
	}
	for _, tt := range tests {
		if got := p.Check(tt.pw, tt.email); !slices.Equal(got, tt.want) {
			t.Errorf("Check(%q) = %v, want %v", tt.pw, got, tt.want)
		}
	}
	tok, hash := auth.NewToken()
	if len(tok) < 40 || auth.HashToken(tok) != hash {
		t.Fatal("token hashing")
	}
}
