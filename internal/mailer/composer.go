package mailer

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/store"
)

//go:embed templates/email.html
var templatesFS embed.FS

var emailTemplate = template.Must(template.ParseFS(templatesFS, "templates/email.html"))

// Branding carries instance wide data used in every e-mail.
type Branding struct {
	AppName  string
	BaseURL  string
	Language string
	Location *time.Location
}

// AlertItem is one certificate listed in an alert e-mail.
type AlertItem struct {
	CertificateID int64
	Name          string
	URL           string
	SubjectCN     string
	IssuerCN      string
	NotAfter      time.Time
	DaysLeft      int
}

type itemView struct {
	Title, Subtitle, Issuer, Link string
	Expires, When                 string
	BadgeBg, BadgeFg              template.CSS
}

type emailView struct {
	Lang, Subject, AppName, Heading  string
	Accent                           template.CSS
	Paragraphs, After, Notes         []string
	Items                            []itemView
	ColEndpoint, ColExpires, ColLeft string
	IssuerLabel                      string
	ButtonURL                        template.URL
	ButtonText, FallbackText, Footer string
}

func render(v *emailView, to, toName string) (*Message, error) {
	var buf bytes.Buffer
	if err := emailTemplate.Execute(&buf, v); err != nil {
		return nil, fmt.Errorf("render e-mail: %w", err)
	}
	return &Message{To: to, ToName: toName, Subject: v.Subject, HTML: buf.String(), Text: plainText(v)}, nil
}

func plainText(v *emailView) string {
	var b strings.Builder
	b.WriteString(v.Heading + "\n" + strings.Repeat("=", len([]rune(v.Heading))) + "\n\n")
	for _, p := range v.Paragraphs {
		b.WriteString(p + "\n\n")
	}
	for _, it := range v.Items {
		fmt.Fprintf(&b, "* %s\n", it.Title)
		if it.Subtitle != "" {
			fmt.Fprintf(&b, "  %s\n", it.Subtitle)
		}
		fmt.Fprintf(&b, "  %s: %s (%s)\n", v.ColExpires, it.Expires, it.When)
		if it.Issuer != "" {
			fmt.Fprintf(&b, "  %s: %s\n", v.IssuerLabel, it.Issuer)
		}
		if it.Link != "" {
			fmt.Fprintf(&b, "  %s\n", it.Link)
		}
		b.WriteString("\n")
	}
	for _, p := range v.After {
		b.WriteString(p + "\n\n")
	}
	if v.ButtonURL != "" {
		fmt.Fprintf(&b, "%s:\n%s\n\n", v.ButtonText, v.ButtonURL)
	}
	for _, p := range v.Notes {
		b.WriteString(p + "\n\n")
	}
	b.WriteString("-- \n" + v.Footer + "\n")
	return b.String()
}

func (br Branding) lang() lang { return newLang(br.Language, br.Location) }

func (br Branding) link(path string) string {
	if br.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(br.BaseURL, "/") + path
}

func (br Branding) appName() string {
	if br.AppName == "" {
		return "Cert Alerter"
	}
	return br.AppName
}

// severity colours: expired/critical, warning, ok.
func badge(daysLeft, criticalDays int) (bg, fg template.CSS) {
	switch {
	case daysLeft < 0:
		return "#7f1d1d", "#ffffff"
	case daysLeft <= criticalDays:
		return "#fee2e2", "#b91c1c"
	case daysLeft <= 30:
		return "#fef3c7", "#b45309"
	}
	return "#dcfce7", "#15803d"
}

func accent(minDays, criticalDays int) template.CSS {
	switch {
	case minDays < 0:
		return "#7f1d1d"
	case minDays <= criticalDays:
		return "#dc2626"
	}
	return "#f59e0b"
}

// AlertParams describe one alert e-mail (one group, one recipient).
type AlertParams struct {
	GroupID       int64
	GroupName     string
	RecipientMail string
	RecipientName string
	CriticalDays  int
	Items         []AlertItem // sorted by urgency
}

// Alert renders an expiry alert for one recipient of one group.
func (br Branding) Alert(p AlertParams) (*Message, error) {
	l := br.lang()
	app := br.appName()
	v := &emailView{
		Lang: l.code, AppName: app, Heading: l.t("alert.heading"),
		ColEndpoint: l.t("alert.col.endpoint"), ColExpires: l.t("alert.col.expires"), ColLeft: l.t("alert.col.left"),
		IssuerLabel: l.t("alert.issuer"),
		Footer:      l.t("alert.footer", "group", p.GroupName, "app", app),
	}
	name := ""
	if p.RecipientName != "" {
		name = ", " + p.RecipientName
	}
	v.Paragraphs = []string{l.t("alert.hello", "name", name), l.t("alert.intro", "group", p.GroupName)}

	minDays := 1 << 30
	for _, it := range p.Items {
		minDays = min(minDays, it.DaysLeft)
		bg, fg := badge(it.DaysLeft, p.CriticalDays)
		title := it.URL
		subtitle := it.SubjectCN
		if it.Name != "" {
			title, subtitle = it.Name, it.URL
		}
		link := ""
		if it.CertificateID > 0 {
			link = br.link("/certificates/" + strconv.FormatInt(it.CertificateID, 10))
		}
		v.Items = append(v.Items, itemView{
			Title: title, Subtitle: subtitle, Issuer: it.IssuerCN, Link: link,
			Expires: l.date(it.NotAfter), When: l.when(it.DaysLeft), BadgeBg: bg, BadgeFg: fg,
		})
	}
	v.Accent = accent(minDays, p.CriticalDays)
	v.After = []string{l.t("alert.action")}
	if u := br.link("/"); u != "" {
		v.ButtonURL = template.URL(u)
		v.ButtonText = l.t("alert.open", "app", app)
	}

	if len(p.Items) == 1 {
		it := p.Items[0]
		host := hostOf(it.URL)
		if it.Name != "" {
			host = l.quote(it.Name) + " (" + host + ")"
		}
		if it.DaysLeft < 0 {
			v.Subject = l.t("alert.subject.one.expired", "group", p.GroupName, "host", host)
		} else {
			v.Subject = l.t("alert.subject.one", "group", p.GroupName, "host", host, "when", l.when(it.DaysLeft))
		}
	} else {
		v.Subject = l.t("alert.subject.many", "group", p.GroupName, "n", strconv.Itoa(len(p.Items)))
	}
	m, err := render(v, p.RecipientMail, p.RecipientName)
	if err != nil {
		return nil, err
	}
	m.Kind, m.GroupID, m.GroupName = store.MailAlert, p.GroupID, p.GroupName
	for _, it := range p.Items {
		m.Certificates = append(m.Certificates, it.URL)
	}
	return m, nil
}

// GroupTest renders a test notification for a group recipient.
func (br Branding) GroupTest(groupID int64, groupName, to, toName string, thresholds []int, items []AlertItem, criticalDays int) (*Message, error) {
	l := br.lang()
	app := br.appName()
	ts := make([]string, len(thresholds))
	for i, t := range thresholds {
		ts[i] = strconv.Itoa(t)
	}
	v := &emailView{
		Lang: l.code, AppName: app, Accent: "#2563eb",
		Subject:     l.t("grouptest.subject", "group", groupName, "app", app),
		Heading:     l.t("grouptest.heading"),
		ColEndpoint: l.t("alert.col.endpoint"), ColExpires: l.t("alert.col.expires"), ColLeft: l.t("alert.col.left"),
		IssuerLabel: l.t("alert.issuer"),
		Paragraphs:  []string{l.t("grouptest.body", "group", groupName, "thresholds", strings.Join(ts, ", "))},
		Footer:      l.t("alert.footer", "group", groupName, "app", app),
	}
	if len(items) == 0 {
		v.Paragraphs = append(v.Paragraphs, l.t("grouptest.none"))
	} else {
		v.Paragraphs = append(v.Paragraphs, l.t("grouptest.certs"))
		for _, it := range items {
			bg, fg := badge(it.DaysLeft, criticalDays)
			title, subtitle := it.URL, it.SubjectCN
			if it.Name != "" {
				title, subtitle = it.Name, it.URL
			}
			when := "—"
			expires := "—"
			if !it.NotAfter.IsZero() {
				when, expires = l.when(it.DaysLeft), l.date(it.NotAfter)
			} else {
				bg, fg = "#e2e8f0", "#475569"
			}
			v.Items = append(v.Items, itemView{Title: title, Subtitle: subtitle, Issuer: it.IssuerCN,
				Link:    br.link("/certificates/" + strconv.FormatInt(it.CertificateID, 10)),
				Expires: expires, When: when, BadgeBg: bg, BadgeFg: fg})
		}
	}
	m, err := render(v, to, toName)
	if err != nil {
		return nil, err
	}
	m.Kind, m.GroupID, m.GroupName = store.MailGroupTest, groupID, groupName
	for _, it := range items {
		m.Certificates = append(m.Certificates, it.URL)
	}
	return m, nil
}

// Test renders a message used to verify SMTP settings.
func (br Branding) Test(to string) (*Message, error) {
	l := br.lang()
	app := br.appName()
	v := &emailView{
		Lang: l.code, AppName: app, Accent: "#16a34a",
		Subject:    l.t("test.subject", "app", app),
		Heading:    l.t("test.heading"),
		Paragraphs: []string{l.t("test.body", "app", app)},
		Footer:     l.t("signature", "app", app) + " · " + l.date(time.Now()),
	}
	return withKind(render(v, to, ""))(store.MailTest)
}

// withKind tags a rendered message with its journal kind.
func withKind(m *Message, err error) func(kind string) (*Message, error) {
	return func(kind string) (*Message, error) {
		if err != nil {
			return nil, err
		}
		m.Kind = kind
		return m, nil
	}
}

// Invite renders an account invitation.
func (br Branding) Invite(to, name, inviter, role, link string, expires time.Time) (*Message, error) {
	l := br.lang()
	app := br.appName()
	v := &emailView{
		Lang: l.code, AppName: app, Accent: "#2563eb",
		Subject: l.t("invite.subject", "app", app),
		Heading: l.t("invite.heading", "app", app),
		Paragraphs: []string{
			l.t("invite.body", "inviter", inviter, "app", app, "role", l.t("role."+role)),
			l.t("invite.action"),
		},
		ButtonURL: template.URL(link), ButtonText: l.t("invite.button"), FallbackText: l.t("link.fallback"),
		Notes:  []string{l.t("invite.expires", "expires", l.date(expires))},
		Footer: l.t("signature", "app", app),
	}
	return withKind(render(v, to, name))(store.MailInvite)
}

// Reset renders a password reset e-mail.
func (br Branding) Reset(to, name, link string, expires time.Time) (*Message, error) {
	l := br.lang()
	app := br.appName()
	v := &emailView{
		Lang: l.code, AppName: app, Accent: "#2563eb",
		Subject:    l.t("reset.subject", "app", app),
		Heading:    l.t("reset.heading"),
		Paragraphs: []string{l.t("reset.body", "email", to)},
		ButtonURL:  template.URL(link), ButtonText: l.t("reset.button"), FallbackText: l.t("link.fallback"),
		Notes:  []string{l.t("reset.expires", "expires", l.date(expires))},
		Footer: l.t("signature", "app", app),
	}
	return withKind(render(v, to, name))(store.MailReset)
}

func hostOf(u string) string {
	s := u
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}
