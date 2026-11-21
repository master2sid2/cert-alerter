package alerts_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

func TestStage(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	th := []int{30, 14, 7, 3}
	day := 24 * time.Hour
	tests := []struct {
		left    time.Duration
		want    int
		wantOK  bool
		expired bool
	}{
		{left: 45 * day, wantOK: false},
		{left: 31*day + time.Hour, wantOK: false},
		{left: 30*day + 23*time.Hour, want: 30, wantOK: true},
		{left: 15 * day, want: 30, wantOK: true},
		{left: 14*day + time.Hour, want: 14, wantOK: true},
		{left: 8 * day, want: 14, wantOK: true},
		{left: 7 * day, want: 7, wantOK: true},
		{left: 4 * day, want: 7, wantOK: true},
		{left: 3*day + time.Hour, want: 3, wantOK: true},
		{left: time.Hour, want: 3, wantOK: true},
		{left: -time.Hour, want: alerts.ExpiredThreshold, wantOK: true, expired: true},
	}
	for _, tt := range tests {
		got, ok := alerts.Stage(now.Add(tt.left), now, th, true)
		if ok != tt.wantOK || (ok && got != tt.want) {
			t.Errorf("left %v: got (%d,%v), want (%d,%v)", tt.left, got, ok, tt.want, tt.wantOK)
		}
	}
	if _, ok := alerts.Stage(now.Add(-time.Hour), now, th, false); ok {
		t.Error("expired stage must be disabled when notifyExpired is false")
	}
}

type fixture struct {
	st     *store.Store
	set    *settings.Service
	eng    *alerts.Engine
	sender *testutil.FakeSender
	now    time.Time
	groups map[string]int64
	rcpts  map[string]int64
	certs  map[string]int64
}

func newFixture(t *testing.T, d *db.DB, smtp bool) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{
		st: store.New(d), sender: &testutil.FakeSender{},
		now:    time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
		groups: map[string]int64{}, rcpts: map[string]int64{}, certs: map[string]int64{},
	}
	f.st.Now = func() time.Time { return f.now }
	box, _ := secret.NewBox(make([]byte, 32))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.set = settings.NewService(f.st, box, "https://certs.test", log)
	if smtp {
		f.configureSMTP(t)
	}
	f.eng = alerts.New(f.st, f.set, &mailer.Journal{Next: f.sender, Store: f.st, Log: log}, log)
	f.eng.Now = func() time.Time { return f.now }

	for _, g := range []string{"A", "B"} {
		grp := &store.Group{Name: "Group " + g}
		if err := f.st.CreateGroup(ctx, grp); err != nil {
			t.Fatal(err)
		}
		f.groups[g] = grp.ID
	}
	member := map[string][]string{"r1": {"A"}, "r2": {"A", "B"}, "r3": {"B"}}
	for _, r := range []string{"r1", "r2", "r3"} {
		rc := &store.Recipient{Email: r + "@example.com"}
		if err := f.st.CreateRecipient(ctx, rc); err != nil {
			t.Fatal(err)
		}
		f.rcpts[r] = rc.ID
		for _, g := range member[r] {
			if _, err := f.st.AddRecipientsToGroup(ctx, f.groups[g], []int64{rc.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	day := 24 * time.Hour
	f.addCert(t, "c1", 5*day+time.Hour, true, "A")
	f.addCert(t, "c2", 20*day+time.Hour, true, "B")
	f.addCert(t, "c3", 2*day+time.Hour, true, "A", "B")
	f.addCert(t, "c4", day, true)              // no group: nobody is alerted
	f.addCert(t, "c5", day, false, "A")        // paused
	f.addCert(t, "c6", 45*day, true, "A", "B") // outside every window
	return f
}

func (f *fixture) configureSMTP(t *testing.T) {
	t.Helper()
	if err := f.set.SaveSMTP(context.Background(), settings.SMTP{Host: "smtp.test", Port: 587, Security: "starttls",
		FromEmail: "certs@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addCert(t *testing.T, name string, left time.Duration, enabled bool, groups ...string) {
	t.Helper()
	ctx := context.Background()
	c := &store.Certificate{URL: "https://" + name + ".example.com", Host: name + ".example.com", Port: 443, Name: name, Enabled: true}
	if err := f.st.CreateCertificate(ctx, c); err != nil {
		t.Fatal(err)
	}
	f.certs[name] = c.ID
	f.renew(t, name, f.now.Add(left))
	if !enabled {
		c.Enabled = false
		if err := f.st.UpdateCertificate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range groups {
		if _, err := f.st.AddCertificatesToGroup(ctx, f.groups[g], []int64{c.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) renew(t *testing.T, name string, notAfter time.Time) {
	t.Helper()
	err := f.st.SaveCheckOutcome(context.Background(), f.certs[name], store.CheckOutcome{
		CheckedAt: f.now, SubjectCN: name + ".example.com", IssuerCN: "Test CA",
		NotBefore: notAfter.AddDate(0, 0, -90), NotAfter: notAfter, Trusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) run(t *testing.T) alerts.Result {
	t.Helper()
	res, err := f.eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// digest summarises delivered messages as recipient -> group -> certificate names.
func digest(msgs []*mailer.Message) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for _, m := range msgs {
		if out[m.To] == nil {
			out[m.To] = map[string][]string{}
		}
		var names []string
		for _, u := range m.Certificates {
			names = append(names, u[len("https://"):len("https://")+2])
		}
		sort.Strings(names)
		out[m.To][m.GroupName] = names
	}
	return out
}

func expectDigest(t *testing.T, got map[string]map[string][]string, want map[string]map[string][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("recipients: got %v, want %v", got, want)
	}
	for r, groups := range want {
		if len(got[r]) != len(groups) {
			t.Fatalf("%s: got %v, want %v", r, got[r], groups)
		}
		for g, certs := range groups {
			if !slices.Equal(got[r][g], certs) {
				t.Fatalf("%s / %s: got %v, want %v", r, g, got[r][g], certs)
			}
		}
	}
}

func TestAlertsArePerGroupAndSentOncePerStage(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		f := newFixture(t, d, true)

		res := f.run(t)
		if res.Sent != 8 || res.Failed != 0 {
			t.Fatalf("first run: %+v", res)
		}
		// Each recipient receives one e-mail per group, listing only the
		// certificates of that group.
		expectDigest(t, digest(f.sender.Messages()), map[string]map[string][]string{
			"r1@example.com": {"Group A": {"c1", "c3"}},
			"r2@example.com": {"Group A": {"c1", "c3"}, "Group B": {"c2", "c3"}},
			"r3@example.com": {"Group B": {"c2", "c3"}},
		})

		// Same stage again: nothing new.
		f.sender.Reset()
		if res := f.run(t); res.Sent != 0 || res.Created != 0 {
			t.Fatalf("second run sent again: %+v\n%s", res, f.sender.Describe())
		}

		// Three days later c1 entered the 3-day stage and c3 has expired;
		// c2 is still in its 30-day stage.
		f.now = f.now.Add(3 * 24 * time.Hour)
		f.sender.Reset()
		f.run(t)
		expectDigest(t, digest(f.sender.Messages()), map[string]map[string][]string{
			"r1@example.com": {"Group A": {"c1", "c3"}},
			"r2@example.com": {"Group A": {"c1", "c3"}, "Group B": {"c3"}},
			"r3@example.com": {"Group B": {"c3"}},
		})

		// Renewing c1 resets its stages: no alert until the new window.
		f.renew(t, "c1", f.now.Add(90*24*time.Hour))
		f.now = f.now.Add(time.Hour)
		f.sender.Reset()
		if f.run(t); len(f.sender.Messages()) != 0 {
			t.Fatalf("renewed certificate alerted:\n%s", f.sender.Describe())
		}

		// The mail journal recorded every delivery.
		entries, total, err := f.st.ListMailLog(context.Background(), store.MailLogFilter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if total != 8 || entries[0].Kind != store.MailAlert || entries[0].Status != store.MailSent {
			t.Fatalf("mail log: total %d, first %+v", total, entries[0])
		}
	})
}

func TestFailedDeliveryIsRetriedWithBackoff(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		f := newFixture(t, d, true)
		f.sender.Fail = func(m *mailer.Message) error {
			if m.To == "r3@example.com" {
				return errors.New("550 mailbox unavailable")
			}
			return nil
		}
		res := f.run(t)
		if res.Failed != 2 || res.Sent != 6 { // r3 had two certificates in one e-mail
			t.Fatalf("run: %+v", res)
		}
		failed, _, err := f.st.ListMailLog(context.Background(), store.MailLogFilter{Status: store.MailFailed})
		if err != nil || len(failed) != 1 || failed[0].Recipient != "r3@example.com" || failed[0].Error == "" {
			t.Fatalf("journal of failures: %+v (%v)", failed, err)
		}

		// Not retried before the backoff elapsed.
		f.sender.Fail = nil
		f.sender.Reset()
		f.run(t)
		if n := len(f.sender.Messages()); n != 0 {
			t.Fatalf("retried too early: %d messages", n)
		}
		f.now = f.now.Add(alerts.Backoff(1) + time.Second)
		f.run(t)
		expectDigest(t, digest(f.sender.Messages()), map[string]map[string][]string{
			"r3@example.com": {"Group B": {"c2", "c3"}},
		})
	})
}

func TestQueuedAlertsWithoutSMTPAndStaleNotifications(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		f := newFixture(t, d, false)
		if res := f.run(t); res.Created == 0 || res.Sent != 0 {
			t.Fatalf("without SMTP alerts must be queued: %+v", res)
		}
		// c1 moves from the 7-day to the 3-day stage while e-mail is down.
		f.now = f.now.Add(2*24*time.Hour + 2*time.Hour)
		f.run(t)
		// r1 leaves group A: its queued alerts become stale.
		if err := f.st.RemoveRecipientFromGroup(ctx, f.groups["A"], f.rcpts["r1"]); err != nil {
			t.Fatal(err)
		}
		f.configureSMTP(t)
		res := f.run(t)
		if res.Cancelled == 0 {
			t.Fatalf("expected superseded/stale notifications to be cancelled: %+v", res)
		}
		if got := f.sender.To("r1@example.com"); len(got) != 0 {
			t.Fatalf("removed recipient was alerted: %v", got[0].Subject)
		}
		// r2 gets a single digest per group with the most urgent stage only.
		if got := f.sender.To("r2@example.com"); len(got) != 2 {
			t.Fatalf("r2 got %d e-mails:\n%s", len(got), f.sender.Describe())
		}
		pending, err := f.st.CountPendingNotifications(ctx)
		if err != nil || pending != 0 {
			t.Fatalf("pending = %d (%v)", pending, err)
		}
	})
}
