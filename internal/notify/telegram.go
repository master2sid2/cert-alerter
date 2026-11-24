package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/master2sid2/cert-alerter/internal/validate"
)

func init() { Register(telegram{}) }

// telegram posts to a chat, group, supergroup topic or channel through the
// Bot API (sendMessage, HTML formatting).
type telegram struct{}

const (
	telegramAPI      = "https://api.telegram.org"
	telegramMaxChars = 4096
)

var (
	telegramToken  = regexp.MustCompile(`^\d{3,}:[A-Za-z0-9_-]{30,}$`)
	telegramChatID = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
	digits         = regexp.MustCompile(`^\d{1,20}$`)
)

func (telegram) Info() Info {
	return Info{
		Type:        "telegram",
		Label:       "Telegram",
		Description: "Messages from a Telegram bot to a chat, group, forum topic or channel.",
		Settings: []Field{
			{Name: "bot_token", Label: "Bot token", Kind: KindSecret, Required: true, MaxLength: 200,
				Placeholder: "123456789:AA…", Help: "Token from @BotFather. One bot serves every Telegram channel."},
			{Name: "api_url", Label: "Bot API URL", Kind: KindURL, Default: telegramAPI, MaxLength: 500,
				Help: "Change only for a local Bot API server or a reverse proxy."},
		},
		Target: []Field{
			{Name: "chat_id", Label: "Chat ID", Kind: KindText, Required: true, MaxLength: 40,
				Placeholder: "-1001234567890 or @channel",
				Help:        "Add the bot to the chat first. Group and channel ids start with -100; public channels may use @name."},
			{Name: "thread_id", Label: "Topic ID", Kind: KindText, MaxLength: 20,
				Help: "Optional: message thread of a forum topic."},
			{Name: "silent", Label: "Send silently", Kind: KindBool,
				Help: "Members receive the message without sound."},
		},
	}
}

func (telegram) Validate(t Target) map[string]string {
	errs := map[string]string{}
	if v := t.Settings["bot_token"]; v != "" && !telegramToken.MatchString(v) {
		errs["bot_token"] = validate.Invalid
	}
	if v := t.Config["chat_id"]; v != "" && !telegramChatID.MatchString(v) {
		errs["chat_id"] = validate.Invalid
	}
	if v := t.Config["thread_id"]; v != "" && !digits.MatchString(v) {
		errs["thread_id"] = validate.Invalid
	}
	return errs
}

func (telegram) Summary(t Target) string {
	s := "chat " + t.Config["chat_id"]
	if th := t.Config["thread_id"]; th != "" {
		s += ", topic " + th
	}
	return s
}

func (telegram) Send(ctx context.Context, c *Client, t Target, msg *Message) error {
	api := strings.TrimRight(t.Settings["api_url"], "/")
	if api == "" {
		api = telegramAPI
	}
	token := t.Settings["bot_token"]
	if token == "" {
		return errors.New("the Telegram bot token is not configured")
	}
	body := map[string]any{
		"chat_id":              t.Config["chat_id"],
		"text":                 telegramText(msg),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
		"disable_notification": t.Config.Bool("silent"),
	}
	if th := t.Config["thread_id"]; th != "" {
		id, _ := strconv.ParseInt(th, 10, 64)
		body["message_thread_id"] = id
	}
	resp, err := c.PostJSON(ctx, api+"/bot"+token+"/sendMessage", body, nil, false)
	if err != nil {
		return err
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(resp.Body, &r)
	if resp.OK() && r.OK {
		return nil
	}
	detail := r.Description
	if r.Parameters.RetryAfter > 0 {
		detail += fmt.Sprintf(" (retry after %d s)", r.Parameters.RetryAfter)
	}
	return HTTPError(resp, detail)
}

var severityEmoji = map[string]string{"expired": "🔴", "critical": "🟠", "warning": "🟡", "": "⚪"}

// telegramText renders the message in Telegram HTML, listing fewer
// certificates when the text would exceed the message size limit.
func telegramText(m *Message) string {
	items, more := m.Shown()
	for {
		s := telegramRender(m, items, more)
		if utf8.RuneCountInString(s) <= telegramMaxChars || len(items) == 0 {
			if utf8.RuneCountInString(s) > telegramMaxChars {
				s = string([]rune(s)[:telegramMaxChars-1]) + "…"
			}
			return s
		}
		items, more = items[:len(items)-1], more+1
	}
}

func telegramRender(m *Message, items []Item, more int) string {
	e := html.EscapeString
	var b strings.Builder
	head := "🔔"
	if m.Kind == KindAlert {
		head = severityEmoji[m.WorstSeverity()]
	}
	fmt.Fprintf(&b, "%s <b>%s</b>\n%s\n", head, e(m.Title()), e(m.Intro()))
	for _, it := range items {
		title := "<b>" + e(it.Title()) + "</b>"
		if it.Link != "" {
			title = `<a href="` + e(it.Link) + `">` + e(it.Title()) + "</a>"
		}
		fmt.Fprintf(&b, "\n%s %s\n", severityEmoji[m.Severity(it)], title)
		if it.Name != "" {
			fmt.Fprintf(&b, "<code>%s</code>\n", e(it.URL))
		}
		fmt.Fprintf(&b, "Expires: %s\n", e(m.Expires(it)))
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n…and %d more\n", more)
	}
	if u := m.GroupURL; u != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">Open group “%s” in %s</a>", e(u), e(m.GroupName), e(m.app()))
	}
	return strings.TrimRight(b.String(), "\n")
}
