package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type captured struct {
	mu      sync.Mutex
	path    string
	auth    string
	body    map[string]any
	status  int
	respond string
}

func (c *captured) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		data, _ := io.ReadAll(r.Body)
		c.path, c.auth = r.URL.Path, r.Header.Get("Authorization")
		c.body = nil
		_ = json.Unmarshal(data, &c.body)
		w.WriteHeader(c.status)
		_, _ = io.WriteString(w, c.respond)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sample() *Message {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	return &Message{
		Kind: KindAlert, AppName: "Cert Alerter", AppURL: "https://certs.test/", GroupName: "Prod <core>",
		GroupURL: "https://certs.test/groups/1", CriticalDays: 7, Location: time.UTC,
		Items: []Item{
			{Name: "API & web", URL: "https://api.example.com", NotAfter: now.Add(-36 * time.Hour), DaysLeft: -2,
				Link: "https://certs.test/certificates/1"},
			{URL: "https://shop.example.com:8443", NotAfter: now.Add(10 * 24 * time.Hour), DaysLeft: 10},
		},
	}
}

func TestRegistry(t *testing.T) {
	var types []string
	for _, p := range Providers() {
		info := p.Info()
		types = append(types, info.Type)
		for _, f := range append(info.Settings, info.Target...) {
			if f.Name == "" || f.Label == "" || f.Kind == "" {
				t.Errorf("%s: incomplete field %+v", info.Type, f)
			}
		}
	}
	if strings.Join(types, ",") != "slack,telegram,webhook" {
		t.Fatalf("providers: %v", types)
	}
}

func TestMessageText(t *testing.T) {
	m := sample()
	if got := m.Title(); got != "[Prod <core>] SSL certificates requiring attention: 2" {
		t.Errorf("title: %q", got)
	}
	if m.WorstSeverity() != "expired" || m.Severity(m.Items[1]) != "warning" {
		t.Errorf("severity: %s / %s", m.WorstSeverity(), m.Severity(m.Items[1]))
	}
	m.Items = m.Items[1:]
	if got := m.Title(); got != "[Prod <core>] SSL certificate shop.example.com:8443 expires in 10 days" {
		t.Errorf("title: %q", got)
	}
	if got := When(-1); got != "expired less than a day ago" {
		t.Errorf("When(-1) = %q", got)
	}
}

func TestTelegram(t *testing.T) {
	c := &captured{status: 200, respond: `{"ok":true}`}
	srv := c.server(t)
	p, _ := Lookup("telegram")
	target := Target{
		Settings: Config{"bot_token": "123456:" + strings.Repeat("A", 35), "api_url": srv.URL},
		Config:   Config{"chat_id": "-1001234567890", "thread_id": "42", "silent": "true"},
	}
	if errs := p.(Validator).Validate(target); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	if err := p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample()); err != nil {
		t.Fatal(err)
	}
	if c.path != "/bot"+target.Settings["bot_token"]+"/sendMessage" {
		t.Errorf("path %q", c.path)
	}
	text, _ := c.body["text"].(string)
	for _, want := range []string{"<b>[Prod &lt;core&gt;] SSL certificates", `<a href="https://certs.test/certificates/1">API &amp; web</a>`,
		"🔴", "🟡", "expired 1 day ago", `href="https://certs.test/groups/1"`} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if c.body["chat_id"] != "-1001234567890" || c.body["message_thread_id"] != float64(42) ||
		c.body["disable_notification"] != true || c.body["parse_mode"] != "HTML" {
		t.Errorf("body %v", c.body)
	}

	// API errors are reported with Telegram's description, never the token.
	c.status, c.respond = 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
	err := p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample())
	if err == nil || err.Error() != "HTTP 400: Bad Request: chat not found" {
		t.Fatalf("error %v", err)
	}
	// Transport errors do not contain the URL (it holds the token).
	target.Settings["api_url"] = "http://127.0.0.1:1"
	err = p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample())
	if err == nil || strings.Contains(err.Error(), "AAAA") || strings.Contains(err.Error(), "/bot") {
		t.Fatalf("error leaks the token: %v", err)
	}

	bad := Target{Settings: Config{"bot_token": "nope"}, Config: Config{"chat_id": "chat", "thread_id": "x"}}
	if errs := p.(Validator).Validate(bad); len(errs) != 3 {
		t.Errorf("expected 3 errors, got %v", errs)
	}
}

func TestTelegramLongMessage(t *testing.T) {
	m := sample()
	m.Items = nil
	for range 40 {
		m.Items = append(m.Items, Item{Name: strings.Repeat("n", 150), URL: "https://" + strings.Repeat("h", 100) + ".example.com",
			NotAfter: time.Now(), DaysLeft: 3})
	}
	text := telegramText(m)
	if n := len([]rune(text)); n > telegramMaxChars {
		t.Fatalf("text has %d characters", n)
	}
	if !strings.Contains(text, "…and ") {
		t.Error("left out items are not summarised")
	}
}

func TestSlack(t *testing.T) {
	c := &captured{status: 200, respond: "ok"}
	srv := c.server(t)
	p, _ := Lookup("slack")
	target := Target{Settings: Config{}, Config: Config{"webhook_url": srv.URL + "/services/T/B/secret"}}
	if err := p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample()); err != nil {
		t.Fatal(err)
	}
	if got := p.(Summarizer).Summary(target); got != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("summary %q", got)
	}
	att := c.body["attachments"].([]any)[0].(map[string]any)
	text := att["text"].(string)
	if att["color"] != "#7f1d1d" || !strings.Contains(text, "*<https://certs.test/certificates/1|API &amp; web>*") ||
		!strings.Contains(c.body["text"].(string), "Prod &lt;core&gt;") {
		t.Errorf("payload %v", c.body)
	}
	c.status, c.respond = 404, "channel_not_found"
	err := p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample())
	if err == nil || err.Error() != "HTTP 404: channel_not_found" {
		t.Fatalf("error %v", err)
	}
}

func TestWebhook(t *testing.T) {
	c := &captured{status: 204}
	srv := c.server(t)
	p, _ := Lookup("webhook")
	target := Target{Settings: Config{}, Config: Config{"url": srv.URL + "/hook", "authorization": "Bearer s3cret"}}
	if err := p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample()); err != nil {
		t.Fatal(err)
	}
	if c.auth != "Bearer s3cret" || c.path != "/hook" {
		t.Errorf("auth %q path %q", c.auth, c.path)
	}
	certs := c.body["certificates"].([]any)
	first := certs[0].(map[string]any)
	if c.body["event"] != "alert" || c.body["severity"] != "expired" || len(certs) != 2 ||
		first["endpoint"] != "https://api.example.com" || first["days_left"] != float64(-2) || first["severity"] != "expired" {
		t.Errorf("payload %v", c.body)
	}
	c.status, c.respond = 500, "boom"
	if err := p.Send(context.Background(), NewClient(nil, 5*time.Second), target, sample()); err == nil || err.Error() != "HTTP 500: boom" {
		t.Fatalf("error %v", err)
	}
}

func TestNormalize(t *testing.T) {
	fields := []Field{
		{Name: "a", Kind: KindText, Required: true},
		{Name: "u", Kind: KindURL},
		{Name: "b", Kind: KindBool},
		{Name: "s", Kind: KindSelect, Options: []Option{{Value: "x"}}, Default: "x"},
	}
	out, errs := Normalize(fields, Config{"a": "  v ", "u": "ftp://x", "b": "yes", "junk": "1"})
	if out["a"] != "v" || out["b"] != "false" || out["s"] != "x" || out["junk"] != "" || errs["u"] != "invalid" || len(errs) != 1 {
		t.Fatalf("out %v errs %v", out, errs)
	}
	if _, errs := Normalize(fields, Config{}); errs["a"] != "required" {
		t.Fatalf("errs %v", errs)
	}
}
