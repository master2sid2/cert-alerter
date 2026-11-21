package mailer

import (
	"fmt"
	"strings"
	"time"
)

// dict holds the e-mail strings per language (keep in sync with
// settings.EmailLanguages). Placeholders use {name}.
var dict = map[string]map[string]string{
	"en": {
		"alert.subject.one":         "[{group}] SSL certificate {host} expires {when}",
		"alert.subject.one.expired": "[{group}] SSL certificate {host} has EXPIRED",
		"alert.subject.many":        "[{group}] SSL certificates requiring attention: {n}",
		"alert.heading":             "Certificate expiration alert",
		"alert.intro":               "The following certificates of group “{group}” require attention:",
		"alert.hello":               "Hello{name},",
		"alert.col.endpoint":        "Endpoint",
		"alert.col.expires":         "Expires",
		"alert.col.left":            "Time left",
		"alert.issuer":              "Issuer",
		"alert.subject":             "Subject",
		"alert.action":              "Please renew the certificates before they expire to avoid service outages.",
		"alert.open":                "Open in {app}",
		"alert.footer":              "You receive this message because you are a recipient of group “{group}” in {app}.",
		"when.in":                   "in {days}",
		"when.expired":              "expired {ago}",
		"when.expired.today":        "expired less than a day ago",
		"ago":                       "{days} ago",
		"days.zero":                 "less than a day",
		"test.subject":              "{app}: test e-mail",
		"test.heading":              "SMTP settings work",
		"test.body":                 "This is a test message from {app}. If you can read it, e-mail delivery is configured correctly.",
		"grouptest.subject":         "[{group}] Test notification from {app}",
		"grouptest.heading":         "Test notification",
		"grouptest.body":            "This is a test notification for group “{group}”. Real alerts are sent when a certificate of this group expires in {thresholds} days.",
		"grouptest.certs":           "Certificates currently in the group:",
		"grouptest.none":            "The group has no certificates yet.",
		"invite.subject":            "You are invited to {app}",
		"invite.heading":            "Welcome to {app}",
		"invite.body":               "{inviter} invited you to {app}, the SSL certificate monitoring service, with the role “{role}”.",
		"invite.action":             "Click the button below to activate your account and choose a password.",
		"invite.button":             "Activate account",
		"invite.expires":            "The link is valid until {expires}.",
		"reset.subject":             "{app}: password reset",
		"reset.heading":             "Reset your password",
		"reset.body":                "A password reset was requested for your account {email}.",
		"reset.button":              "Choose a new password",
		"reset.expires":             "The link is valid until {expires}. If you did not request a reset, ignore this e-mail.",
		"link.fallback":             "If the button does not work, copy this link into your browser:",
		"role.admin":                "Administrator",
		"role.user":                 "User",
		"signature":                 "— {app}",
	},
}

type lang struct {
	code string
	loc  *time.Location
}

func newLang(code string, loc *time.Location) lang {
	if _, ok := dict[code]; !ok {
		code = "en"
	}
	if loc == nil {
		loc = time.UTC
	}
	return lang{code: code, loc: loc}
}

// t translates key and substitutes {placeholders} from kv pairs.
func (l lang) t(key string, kv ...string) string {
	s, ok := dict[l.code][key]
	if !ok {
		s = dict["en"][key]
	}
	if len(kv) > 0 {
		pairs := make([]string, 0, len(kv))
		for i := 0; i+1 < len(kv); i += 2 {
			pairs = append(pairs, "{"+kv[i]+"}", kv[i+1])
		}
		s = strings.NewReplacer(pairs...).Replace(s)
	}
	return s
}

// quote wraps s in the language's quotation marks.
func (l lang) quote(s string) string {
	return "“" + s + "”"
}

// days renders a duration in whole days with correct plural forms.
func (l lang) days(n int) string {
	if n <= 0 {
		return l.t("days.zero")
	}
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

// when renders "in 5 days" / "expired 2 days ago".
func (l lang) when(daysLeft int) string {
	if daysLeft >= 0 {
		return l.t("when.in", "days", l.days(daysLeft))
	}
	ago := -daysLeft - 1
	if ago == 0 {
		return l.t("when.expired.today")
	}
	return l.t("when.expired", "ago", l.t("ago", "days", l.days(ago)))
}

// date formats a timestamp in the configured zone.
func (l lang) date(t time.Time) string {
	return t.In(l.loc).Format("Jan 2, 2006 15:04 MST")
}
