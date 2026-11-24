package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/config"
	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/logging"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/monitor"
	"github.com/master2sid2/cert-alerter/internal/notify"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/server"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/testutil"
	"github.com/master2sid2/cert-alerter/web"
)

type env struct {
	t      *testing.T
	url    string
	srv    *server.Server
	st     *store.Store
	set    *settings.Service
	sender *testutil.FakeSender
	ca     *testutil.CA
	access *lockedBuffer // JSON access log
}

// lockedBuffer is a bytes.Buffer safe for concurrent writers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEnv(t *testing.T, d *db.DB) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(d)
	box, _ := secret.NewBox(make([]byte, 32))
	set := settings.NewService(st, box, "", log)
	sender := &testutil.FakeSender{}
	journal := &mailer.Journal{Next: sender, Store: st, Log: log}
	ca := testutil.NewCA(t)
	channels := notify.NewService(st, box, notify.NewClient(ca.Pool, 5*time.Second), log)
	eng := alerts.New(st, set, journal, log)
	eng.Channels = channels
	mon := monitor.New(ctx, st, set, eng, log, "test", false)
	mon.Roots = ca.Pool
	cfg := &config.Config{CookieSecure: "auto"}
	access := &lockedBuffer{}
	accessLog, err := logging.NewAccessLog(access, logging.FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(ctx, server.Deps{Config: cfg, Store: st, Settings: set, Monitor: mon, Alerts: eng,
		Channels: channels, Sender: journal, Log: log, AccessLog: accessLog, Static: web.Static(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return &env{t: t, url: hs.URL, srv: srv, st: st, set: set, sender: sender, ca: ca, access: access}
}

type client struct {
	e    *env
	http *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

// do sends a JSON request and decodes the JSON response into out (if not nil).
func (c *client) do(method, path string, body any, out any, headers ...string) int {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.url+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			c.e.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return res.StatusCode
}

type apiErr struct {
	Error struct {
		Code    string            `json:"code"`
		Fields  map[string]string `json:"fields"`
		Details map[string]any    `json:"details"`
	} `json:"error"`
}

// setupAdmin completes the first-run setup and returns the admin client.
func (e *env) setupAdmin() *client {
	e.t.Helper()
	c := e.client()
	token := e.srv.SetupToken()
	if token == "" {
		e.t.Fatal("expected a setup token for an empty database")
	}
	var er apiErr
	if code := c.do("POST", "/api/setup", map[string]string{"token": "wrong", "email": "a@example.com", "password": "Root-Passw0rd-26"}, &er); code != 403 || er.Error.Code != "invalid_setup_token" {
		e.t.Fatalf("wrong token: %d %+v", code, er)
	}
	if code := c.do("POST", "/api/setup", map[string]string{"token": token, "email": "a@example.com", "password": "short"}, &er); code != 422 || er.Error.Code != "weak_password" {
		e.t.Fatalf("weak password: %d %+v", code, er)
	}
	if code := c.do("POST", "/api/setup", map[string]string{"token": token, "email": "admin@example.com", "name": "Admin",
		"password": "Root-Passw0rd-26", "base_url": "https://certs.example.com"}, nil); code != 201 {
		e.t.Fatalf("setup: %d", code)
	}
	if code := c.do("POST", "/api/setup", map[string]string{"token": token, "email": "x@example.com", "password": "Root-Passw0rd-26"}, nil); code != 409 {
		e.t.Fatalf("second setup: %d", code)
	}
	cfg, _ := e.set.Get(context.Background())
	if cfg.General.BaseURL != "https://certs.example.com" {
		e.t.Fatalf("base URL from setup not stored: %q", cfg.General.BaseURL)
	}
	if err := e.set.SaveSMTP(context.Background(), settings.SMTP{Host: "smtp.example.com", Port: 587, Security: "starttls",
		FromEmail: "certs@example.com"}, nil); err != nil {
		e.t.Fatal(err)
	}
	return c
}

var linkRe = regexp.MustCompile(`https://certs\.example\.com/(invite|reset)/([A-Za-z0-9_-]+)`)

func TestSetupInviteAndPermissions(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		e := newEnv(t, d)
		admin := e.setupAdmin()

		// Admin creates the structure.
		var g struct{ ID int64 }
		if code := admin.do("POST", "/api/groups", map[string]string{"name": "Prod"}, &g); code != 201 {
			t.Fatalf("create group: %d", code)
		}
		if code := admin.do("POST", "/api/groups/"+itoa(g.ID)+"/recipients", map[string]string{"text": "ops@example.com, bad@"}, nil); code != 422 {
			t.Fatalf("invalid recipient accepted: %d", code)
		}
		if code := admin.do("POST", "/api/groups/"+itoa(g.ID)+"/recipients", map[string]string{"text": "ops@example.com\noncall@example.com"}, nil); code != 200 {
			t.Fatalf("add recipients: %d", code)
		}

		// Invite a regular user; the link arrives by e-mail.
		var inv struct {
			Invite struct {
				URL       string `json:"url"`
				EmailSent bool   `json:"email_sent"`
			} `json:"invite"`
		}
		if code := admin.do("POST", "/api/users", map[string]string{"email": "user@example.com", "role": "user"}, &inv); code != 201 || !inv.Invite.EmailSent {
			t.Fatalf("invite: %d %+v", code, inv)
		}
		mails := e.sender.To("user@example.com")
		if len(mails) != 1 || mails[0].Kind != store.MailInvite {
			t.Fatalf("invite e-mail: %+v", mails)
		}
		m := linkRe.FindStringSubmatch(mails[0].Text)
		if m == nil || m[0] != inv.Invite.URL {
			t.Fatalf("link in e-mail %q vs API %q", m, inv.Invite.URL)
		}
		user := e.client()
		var er apiErr
		if code := user.do("POST", "/api/auth/token/"+m[2], map[string]string{"password": "User-Passw0rd"}, &er); code != 422 ||
			er.Error.Details["violations"].([]any)[0] != "contains_email" {
			t.Fatalf("password containing the e-mail name accepted: %d %+v", code, er)
		}
		if code := user.do("POST", "/api/auth/token/"+m[2], map[string]string{"name": "Ivan", "password": "Str0ng-Passphrase"}, nil); code != 200 {
			t.Fatalf("accept invite: %d", code)
		}
		if code := e.client().do("POST", "/api/auth/token/"+m[2], map[string]string{"password": "Str0ng-Passphrase"}, nil); code != 404 {
			t.Fatalf("invite link reusable: %d", code)
		}

		// The regular user may view, add certificates and put them into groups...
		port := e.ca.Serve(t, time.Now().Add(10*24*time.Hour))
		var cert struct {
			ID       int64
			Status   string
			DaysLeft int    `json:"days_left"`
			Trusted  bool   `json:"trusted"`
			URL      string `json:"url"`
		}
		if code := user.do("POST", "/api/certificates", map[string]any{"url": "127.0.0.1:" + itoa(int64(port)), "group_ids": []int64{g.ID}}, &cert); code != 201 {
			t.Fatalf("user adds certificate: %d", code)
		}
		if cert.Status != store.StatusWarning || cert.DaysLeft != 9 || !cert.Trusted {
			t.Fatalf("checked certificate: %+v", cert)
		}
		if code := user.do("POST", "/api/certificates", map[string]any{"url": "https://127.0.0.1:" + itoa(int64(port)) + "/other"}, &er); code != 409 || er.Error.Code != "duplicate" {
			t.Fatalf("duplicate endpoint: %d %+v", code, er)
		}
		for _, path := range []string{"/api/dashboard", "/api/certificates", "/api/groups", "/api/recipients", "/api/notifications", "/api/mail-log"} {
			if code := user.do("GET", path, nil, nil); code != 200 {
				t.Errorf("user GET %s: %d", path, code)
			}
		}
		// ...but cannot delete or administer anything.
		forbidden := [][2]string{
			{"DELETE", "/api/groups/" + itoa(g.ID)},
			{"PUT", "/api/groups/" + itoa(g.ID)},
			{"POST", "/api/groups"},
			{"DELETE", "/api/certificates/" + itoa(cert.ID)},
			{"PUT", "/api/certificates/" + itoa(cert.ID)},
			{"DELETE", "/api/groups/" + itoa(g.ID) + "/certificates/" + itoa(cert.ID)},
			{"POST", "/api/groups/" + itoa(g.ID) + "/recipients"},
			{"DELETE", "/api/groups/" + itoa(g.ID) + "/recipients/1"},
			{"POST", "/api/recipients"},
			{"DELETE", "/api/recipients/1"},
			{"GET", "/api/users"},
			{"POST", "/api/users"},
			{"GET", "/api/settings"},
			{"PUT", "/api/settings/security"},
			{"GET", "/api/audit"},
			{"POST", "/api/groups/" + itoa(g.ID) + "/test"},
		}
		for _, f := range forbidden {
			if code := user.do(f[0], f[1], map[string]string{"name": "x", "email": "x@example.com", "text": "x@example.com"}, nil); code != 403 {
				t.Errorf("user %s %s: %d, want 403", f[0], f[1], code)
			}
		}
		// Invites and resets are hidden from regular users in the mail log.
		var ml struct{ Total int }
		user.do("GET", "/api/mail-log?kind=invite", nil, &ml)
		if ml.Total != 0 {
			t.Fatalf("user sees invite e-mails: %d", ml.Total)
		}
		admin.do("GET", "/api/mail-log?kind=invite", nil, &ml)
		if ml.Total != 1 {
			t.Fatalf("admin mail log invites: %d", ml.Total)
		}

		// Admin guard rails.
		var me struct{ User struct{ ID int64 } }
		admin.do("GET", "/api/me", nil, &me)
		if code := admin.do("PUT", "/api/users/"+itoa(me.User.ID), map[string]string{"role": "user"}, &er); code != 409 || er.Error.Code != "self_change" {
			t.Fatalf("self demotion: %d %+v", code, er)
		}
		if code := admin.do("DELETE", "/api/users/"+itoa(me.User.ID), nil, &er); code != 409 {
			t.Fatalf("self deletion: %d", code)
		}

		// Disabling the user ends its session immediately.
		var users []struct {
			ID    int64
			Email string
		}
		admin.do("GET", "/api/users", nil, &users)
		var uid int64
		for _, u := range users {
			if u.Email == "user@example.com" {
				uid = u.ID
			}
		}
		if code := admin.do("PUT", "/api/users/"+itoa(uid), map[string]string{"status": "disabled"}, nil); code != 200 {
			t.Fatalf("disable: %d", code)
		}
		if code := user.do("GET", "/api/me", nil, nil); code != 401 {
			t.Fatalf("disabled user still signed in: %d", code)
		}
	})
}

func TestLoginLockoutPasswordExpiryAndCSRF(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		e := newEnv(t, d)
		admin := e.setupAdmin()

		anon := e.client()
		var er apiErr
		for i := range 5 {
			code := anon.do("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "nope"}, &er)
			want := "invalid_credentials"
			if i == 4 {
				want = "account_locked"
			}
			if code != 401 || er.Error.Code != want {
				t.Fatalf("attempt %d: %d %s", i+1, code, er.Error.Code)
			}
		}
		if code := anon.do("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "Root-Passw0rd-26"}, &er); er.Error.Code != "account_locked" {
			t.Fatalf("locked account signed in: %d", code)
		}
		if code := anon.do("POST", "/api/auth/login", map[string]string{"email": "ghost@example.com", "password": "x"}, &er); code != 401 || er.Error.Code != "invalid_credentials" {
			t.Fatalf("unknown user: %d %+v", code, er)
		}

		// Cross-site browser requests are rejected.
		if code := admin.do("POST", "/api/groups", map[string]string{"name": "X"}, &er, "Sec-Fetch-Site", "cross-site",
			"Origin", "https://evil.example"); code != 403 || er.Error.Code != "cross_origin" {
			t.Fatalf("cross-site request: %d %+v", code, er)
		}

		// An expired password restricts the session to changing it.
		sec := settings.Defaults().Security
		sec.Password.MaxAgeDays = 1
		if err := e.set.SaveSecurity(context.Background(), sec); err != nil {
			t.Fatal(err)
		}
		u, _ := e.st.GetUserByEmail(context.Background(), "admin@example.com")
		e.st.Now = func() time.Time { return time.Now().Add(-48 * time.Hour) }
		if err := e.st.SetUserPassword(context.Background(), u.ID, u.PasswordHash, false, 3); err != nil {
			t.Fatal(err)
		}
		e.st.Now = time.Now
		if code := admin.do("GET", "/api/certificates", nil, &er); code != 403 || er.Error.Code != "password_change_required" {
			t.Fatalf("expired password: %d %+v", code, er)
		}
		var me struct {
			MustChange bool `json:"must_change_password"`
		}
		if admin.do("GET", "/api/me", nil, &me); !me.MustChange {
			t.Fatal("must_change_password not reported")
		}
		if code := admin.do("POST", "/api/me/password", map[string]string{"current_password": "Root-Passw0rd-26", "new_password": "Root-Passw0rd-26"}, &er); code != 422 {
			t.Fatalf("password reuse accepted: %d", code)
		}
		if code := admin.do("POST", "/api/me/password", map[string]string{"current_password": "Root-Passw0rd-26", "new_password": "New-Secret-2026"}, nil); code != 200 {
			t.Fatalf("change password: %d", code)
		}
		if code := admin.do("GET", "/api/certificates", nil, nil); code != 200 {
			t.Fatalf("after change: %d", code)
		}
	})
}

func TestStaticAssetsAndUnknownAPI(t *testing.T) {
	e := newEnv(t, testutil.SQLite(t))
	for _, path := range []string{"/", "/certificates/42", "/invite/abc"} {
		res, err := http.Get(e.url + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), `<div id="app">`) {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Fatalf("missing CSP: %q", csp)
		}
	}
	req, _ := http.NewRequest("GET", e.url+"/assets/js/main.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("main.js: %d %v", res.StatusCode, res.Header)
	}
	etag := res.Header.Get("ETag")
	req.Header.Set("If-None-Match", etag)
	res, _ = http.DefaultTransport.RoundTrip(req)
	res.Body.Close()
	if res.StatusCode != 304 {
		t.Fatalf("conditional request: %d", res.StatusCode)
	}
	res, _ = http.Get(e.url + "/assets/missing.js")
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("missing asset: %d", res.StatusCode)
	}
	var er apiErr
	if code := e.client().do("GET", "/api/does-not-exist", nil, &er); code != 404 || er.Error.Code != "not_found" {
		t.Fatalf("unknown API: %d", code)
	}
	if code := e.client().do("GET", "/api/certificates", nil, &er); code != 401 {
		t.Fatalf("anonymous API access: %d", code)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
