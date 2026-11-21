package mailer

import (
	"strings"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/store"
)

func TestAlertMessage(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	// An unsupported language (e.g. one stored by an older version) falls back to English.
	br := Branding{AppName: "Cert Alerter", BaseURL: "https://certs.example.com", Language: "xx", Location: tokyo}
	na := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	one, err := br.Alert(AlertParams{GroupID: 7, GroupName: "Prod", RecipientMail: "ops@example.com", CriticalDays: 7,
		Items: []AlertItem{{CertificateID: 3, Name: "Portal", URL: "https://portal.example.com/login", NotAfter: na, DaysLeft: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	if one.Subject != "[Prod] SSL certificate “Portal” (portal.example.com) expires in 5 days" {
		t.Fatalf("subject %q", one.Subject)
	}
	if one.Kind != store.MailAlert || one.GroupID != 7 || one.Certificates[0] != "https://portal.example.com/login" {
		t.Fatalf("metadata: %+v", one)
	}
	for _, want := range []string{"Nov 10, 2026 18:00 JST", "https://certs.example.com/certificates/3", "“Prod”"} {
		if !strings.Contains(one.Text, want) || !strings.Contains(one.HTML, want) {
			t.Errorf("message lacks %q", want)
		}
	}

	many, err := br.Alert(AlertParams{GroupName: "Prod", RecipientMail: "ops@example.com", Items: []AlertItem{
		{URL: "https://a.example.com", NotAfter: na, DaysLeft: -3},
		{URL: "https://b.example.com", NotAfter: na, DaysLeft: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if many.Subject != "[Prod] SSL certificates requiring attention: 2" ||
		!strings.Contains(many.Text, "expired 2 days ago") || !strings.Contains(many.Text, "in 1 day") {
		t.Fatalf("english digest: %q\n%s", many.Subject, many.Text)
	}
	// User controlled values are escaped in the HTML part.
	x, _ := br.Alert(AlertParams{GroupName: "<script>alert(1)</script>", RecipientMail: "a@b.c",
		Items: []AlertItem{{URL: "https://x.example.com", NotAfter: na, DaysLeft: 2}}})
	if strings.Contains(x.HTML, "<script>alert(1)</script>") {
		t.Fatal("HTML not escaped")
	}
}
