package notify

import (
	"context"
	"time"

	"github.com/master2sid2/cert-alerter/internal/validate"
)

func init() { Register(webhook{}) }

// webhook posts a JSON document describing the alert to any URL (n8n,
// Node-RED, Home Assistant, custom scripts…).
type webhook struct{}

func (webhook) Info() Info {
	return Info{
		Type:        "webhook",
		Label:       "Webhook",
		Description: "JSON POST request to any URL, for automation tools and custom integrations.",
		Target: []Field{
			{Name: "url", Label: "URL", Kind: KindSecret, Required: true, MaxLength: 1000,
				Placeholder: "https://automation.example.com/hooks/certs",
				Help:        "Receives a POST with a JSON body (event, group, title, text, certificates). Stored encrypted."},
			{Name: "authorization", Label: "Authorization header", Kind: KindSecret, MaxLength: 1000,
				Placeholder: "Bearer …", Help: "Optional value of the Authorization header."},
			{Name: "skip_verify", Label: "Skip TLS verification", Kind: KindBool,
				Help: "Only for internal endpoints with a certificate that cannot be verified."},
		},
	}
}

func (webhook) Validate(t Target) map[string]string {
	if v := t.Config["url"]; v != "" && !ValidURL(v) {
		return map[string]string{"url": validate.Invalid}
	}
	return nil
}

func (webhook) Summary(t Target) string { return HostOf(t.Config["url"]) }

// WebhookPayload is the JSON document posted by the webhook provider.
type WebhookPayload struct {
	Event        string               `json:"event"` // "alert" or "test"
	App          string               `json:"app"`
	AppURL       string               `json:"app_url,omitempty"`
	Group        WebhookGroup         `json:"group"`
	Severity     string               `json:"severity,omitempty"` // worst: expired, critical, warning
	Title        string               `json:"title"`
	Text         string               `json:"text"` // plain text rendering of the whole message
	Certificates []WebhookCertificate `json:"certificates"`
}

// WebhookGroup identifies the alert group.
type WebhookGroup struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// WebhookCertificate is one certificate of the alert.
type WebhookCertificate struct {
	Name      string     `json:"name,omitempty"`
	Endpoint  string     `json:"endpoint"`
	SubjectCN string     `json:"subject_cn,omitempty"`
	IssuerCN  string     `json:"issuer_cn,omitempty"`
	NotAfter  *time.Time `json:"not_after"`
	DaysLeft  *int       `json:"days_left"`
	Severity  string     `json:"severity,omitempty"`
	URL       string     `json:"url,omitempty"` // page in the web interface
}

// NewWebhookPayload builds the payload of a message.
func NewWebhookPayload(m *Message) WebhookPayload {
	p := WebhookPayload{
		Event: "alert", App: m.app(), AppURL: m.AppURL,
		Group: WebhookGroup{Name: m.GroupName, URL: m.GroupURL},
		Title: m.Title(), Text: m.PlainText(), Certificates: []WebhookCertificate{},
	}
	if m.Kind != KindAlert {
		p.Event = "test"
	} else {
		p.Severity = m.WorstSeverity()
	}
	for _, it := range m.Items {
		c := WebhookCertificate{Name: it.Name, Endpoint: it.URL, SubjectCN: it.SubjectCN, IssuerCN: it.IssuerCN,
			Severity: m.Severity(it), URL: it.Link}
		if !it.NotAfter.IsZero() {
			c.NotAfter = new(it.NotAfter.UTC())
			c.DaysLeft = new(it.DaysLeft)
		}
		p.Certificates = append(p.Certificates, c)
	}
	return p
}

func (webhook) Send(ctx context.Context, c *Client, t Target, msg *Message) error {
	var headers map[string]string
	if a := t.Config["authorization"]; a != "" {
		headers = map[string]string{"Authorization": a}
	}
	resp, err := c.PostJSON(ctx, t.Config["url"], NewWebhookPayload(msg), headers, t.Config.Bool("skip_verify"))
	if err != nil {
		return err
	}
	if !resp.OK() {
		return HTTPError(resp, "")
	}
	return nil
}
