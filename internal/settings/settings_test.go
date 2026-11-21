package settings

import (
	"errors"
	"slices"
	"testing"

	"github.com/master2sid2/cert-alerter/internal/validate"
)

func TestValidateMonitoringNormalisesThresholds(t *testing.T) {
	m := Defaults().Monitoring
	m.Thresholds = []int{3, 30, 7, 14, 7}
	if err := ValidateMonitoring(&m); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Thresholds, []int{30, 14, 7, 3}) {
		t.Fatalf("thresholds %v", m.Thresholds)
	}
	m.Thresholds = []int{0, 400}
	err := ValidateMonitoring(&m)
	fe, ok := errors.AsType[validate.Errors](err)
	if !ok || fe["thresholds"] == "" {
		t.Fatalf("invalid thresholds accepted: %v", err)
	}
}

func TestValidateSMTPAndGeneral(t *testing.T) {
	s := SMTP{Host: "smtp.example.com", Port: 465, Security: "tls", FromEmail: "Certs@Example.com"}
	if err := ValidateSMTP(&s); err != nil || s.FromEmail != "certs@example.com" || s.AuthMethod != "auto" {
		t.Fatalf("valid SMTP rejected: %v %+v", err, s)
	}
	s = SMTP{Host: "smtp.example.com", Port: 0, Security: "ssl"}
	fe, _ := errors.AsType[validate.Errors](ValidateSMTP(&s))
	for _, f := range []string{"port", "security", "from_email"} {
		if fe[f] == "" {
			t.Errorf("missing error for %s: %v", f, fe)
		}
	}
	g := General{AppName: "X", BaseURL: "https://certs.example.com/", EmailLanguage: "en", Timezone: "Asia/Tokyo"}
	if err := ValidateGeneral(&g); err != nil || g.BaseURL != "https://certs.example.com" {
		t.Fatalf("general: %v %+v", err, g)
	}
	g.BaseURL, g.Timezone, g.EmailLanguage = "ftp://x", "Mars/Olympus", "xx"
	fe, _ = errors.AsType[validate.Errors](ValidateGeneral(&g))
	if fe["base_url"] == "" || fe["timezone"] == "" || fe["email_language"] == "" {
		t.Fatalf("invalid general accepted: %v", fe)
	}
}
