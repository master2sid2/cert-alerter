package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/master2sid2/cert-alerter/internal/validate"
)

func init() { Register(slack{}) }

// slack posts to an incoming webhook. The payload (text plus one coloured
// attachment) is also understood by Mattermost, Rocket.Chat and the
// Slack-compatible endpoint of Discord webhooks (".../slack").
type slack struct{}

func (slack) Info() Info {
	return Info{
		Type:        "slack",
		Label:       "Slack",
		Description: "Incoming webhook of a Slack channel. Mattermost, Rocket.Chat and Discord (webhook URL + /slack) accept the same format.",
		Target: []Field{
			{Name: "webhook_url", Label: "Webhook URL", Kind: KindSecret, Required: true, MaxLength: 1000,
				Placeholder: "https://hooks.slack.com/services/T000/B000/XXXX",
				Help:        "Slack: Apps → Incoming Webhooks → Add New Webhook to Workspace. The URL is stored encrypted."},
			{Name: "skip_verify", Label: "Skip TLS verification", Kind: KindBool,
				Help: "Only for self-hosted servers with a certificate that cannot be verified."},
		},
	}
}

func (slack) Validate(t Target) map[string]string {
	if v := t.Config["webhook_url"]; v != "" && !ValidURL(v) {
		return map[string]string{"webhook_url": validate.Invalid}
	}
	return nil
}

func (slack) Summary(t Target) string { return HostOf(t.Config["webhook_url"]) }

var slackColor = map[string]string{"expired": "#7f1d1d", "critical": "#dc2626", "warning": "#f59e0b", "": "#2563eb"}

func (slack) Send(ctx context.Context, c *Client, t Target, msg *Message) error {
	resp, err := c.PostJSON(ctx, t.Config["webhook_url"], slackPayload(msg), nil, t.Config.Bool("skip_verify"))
	if err != nil {
		return err
	}
	if !resp.OK() {
		return HTTPError(resp, "")
	}
	return nil
}

func slackPayload(m *Message) map[string]any {
	e := slackEscape
	var b strings.Builder
	b.WriteString(e(m.Intro()) + "\n")
	items, more := m.Shown()
	for _, it := range items {
		title := "*" + e(it.Title()) + "*"
		if it.Link != "" {
			title = "*<" + it.Link + "|" + e(it.Title()) + ">*"
		}
		fmt.Fprintf(&b, "\n%s %s\n", severityEmoji[m.Severity(it)], title)
		if it.Name != "" {
			fmt.Fprintf(&b, "`%s`\n", strings.ReplaceAll(e(it.URL), "`", "'"))
		}
		fmt.Fprintf(&b, "Expires: %s\n", e(m.Expires(it)))
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n…and %d more\n", more)
	}
	if m.GroupURL != "" {
		fmt.Fprintf(&b, "\n<%s|Open group “%s” in %s>", m.GroupURL, e(m.GroupName), e(m.app()))
	}
	color := slackColor[""]
	if m.Kind == KindAlert {
		color = slackColor[m.WorstSeverity()]
	}
	return map[string]any{
		"text": "*" + e(m.Title()) + "*",
		"attachments": []map[string]any{{
			"color":     color,
			"fallback":  m.PlainText(),
			"text":      strings.TrimRight(b.String(), "\n"),
			"mrkdwn_in": []string{"text"},
		}},
	}
}

// slackEscape escapes the control characters of Slack mrkdwn; "|" would
// end the text of a link.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "¦").Replace(s)
}
