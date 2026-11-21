package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

func TestUsersAndTokens(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		st := store.New(d)
		u := &store.User{Email: "  Admin@Example.COM ", Name: "Admin", Role: store.RoleAdmin, Status: store.UserActive, PasswordHash: "h1"}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if u.Email != "admin@example.com" {
			t.Fatalf("email not normalised: %q", u.Email)
		}
		if err := st.CreateUser(ctx, &store.User{Email: "ADMIN@example.com", Role: store.RoleUser, Status: store.UserInvited}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate e-mail: err = %v", err)
		}
		got, err := st.GetUserByEmail(ctx, "admin@EXAMPLE.com")
		if err != nil || got.ID != u.ID {
			t.Fatalf("lookup: %v %v", got, err)
		}
		if n, _ := st.CountActiveAdmins(ctx); n != 1 {
			t.Fatalf("active admins = %d", n)
		}

		// Password history keeps only the configured number of entries.
		for _, h := range []string{"h2", "h3", "h4"} {
			if err := st.SetUserPassword(ctx, u.ID, h, false, 2); err != nil {
				t.Fatal(err)
			}
		}
		hist, err := st.PasswordHistory(ctx, u.ID, 10)
		if err != nil || len(hist) != 2 || hist[0] != "h4" || hist[1] != "h3" {
			t.Fatalf("history = %v (%v)", hist, err)
		}

		// Failed logins and lockout.
		for range 3 {
			if _, err := st.IncrementFailedLogins(ctx, u.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.LockUser(ctx, u.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, _ = st.GetUser(ctx, u.ID)
		if !got.IsLocked(time.Now()) || got.FailedLogins != 0 {
			t.Fatalf("lock: %+v", got)
		}
		_ = st.UnlockUser(ctx, u.ID)

		// Tokens are single use and replace older ones of the same purpose.
		tok := &store.UserToken{UserID: u.ID, Purpose: store.TokenReset, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
		if err := st.CreateUserToken(ctx, tok, "hash-1"); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateUserToken(ctx, &store.UserToken{UserID: u.ID, Purpose: store.TokenReset, CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour)}, "hash-2"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetUserTokenByHash(ctx, "hash-1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old token still valid: %v", err)
		}
		t2, err := st.GetUserTokenByHash(ctx, "hash-2")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.ConsumeUserToken(ctx, t2.ID); !ok {
			t.Fatal("first consume failed")
		}
		if ok, _ := st.ConsumeUserToken(ctx, t2.ID); ok {
			t.Fatal("token consumed twice")
		}

		// Deleting a user cascades to sessions.
		sess := &store.Session{ID: "s1", UserID: u.ID, CreatedAt: time.Now(), LastSeenAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
		if err := st.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteUser(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetSession(ctx, "s1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("session survived user deletion: %v", err)
		}
	})
}

func TestGroupsCertificatesAndRecipients(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		st := store.New(d)
		g := &store.Group{Name: "Παραγωγή"}
		if err := st.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		// Names are unique case-insensitively, also beyond ASCII.
		if err := st.CreateGroup(ctx, &store.Group{Name: "παραγωγή"}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("case-insensitive duplicate: %v", err)
		}
		c := &store.Certificate{URL: "https://example.com", Host: "example.com", Port: 443, Enabled: true}
		if err := st.CreateCertificate(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateCertificate(ctx, &store.Certificate{URL: "example.com", Host: "example.com", Port: 443}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate endpoint: %v", err)
		}
		n, err := st.AddCertificatesToGroup(ctx, g.ID, []int64{c.ID, c.ID}, nil)
		if err != nil || n != 1 {
			t.Fatalf("add = %d, %v", n, err)
		}
		if n, _ := st.AddCertificatesToGroup(ctx, g.ID, []int64{c.ID}, nil); n != 0 {
			t.Fatalf("re-adding counted as new: %d", n)
		}
		if _, err := st.AddCertificatesToGroup(ctx, g.ID, []int64{9999}, nil); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown certificate: %v", err)
		}
		r, created, err := st.EnsureRecipient(ctx, "Ops@Example.com", "")
		if err != nil || !created {
			t.Fatalf("ensure: %v %v", created, err)
		}
		if again, created, _ := st.EnsureRecipient(ctx, "ops@example.com", ""); created || again.ID != r.ID {
			t.Fatal("recipient duplicated")
		}
		if _, err := st.AddRecipientsToGroup(ctx, g.ID, []int64{r.ID}); err != nil {
			t.Fatal(err)
		}
		gg, err := st.GetGroup(ctx, g.ID)
		if err != nil || gg.CertificateCount != 1 || gg.RecipientCount != 1 {
			t.Fatalf("counts: %+v %v", gg, err)
		}

		// Check results persist; failed checks keep the last good data.
		na := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)
		if err := st.SaveCheckOutcome(ctx, c.ID, store.CheckOutcome{CheckedAt: time.Now(), SubjectCN: "example.com",
			SANs: []string{"example.com", "www.example.com"}, NotBefore: na.AddDate(0, -3, 0), NotAfter: na, Trusted: true,
			Chain: []store.ChainCert{{Subject: "example.com", Issuer: "CA", NotAfter: na}}}); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveCheckOutcome(ctx, c.ID, store.CheckOutcome{CheckedAt: time.Now(), Error: "connection refused"}); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetCertificate(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.NotAfter == nil || !got.NotAfter.Equal(na) || got.LastError != "connection refused" || len(got.SANs) != 2 ||
			len(got.Chain) != 1 || !got.Trusted || len(got.GroupIDs) != 1 {
			t.Fatalf("certificate: %+v", got)
		}
		if st := got.Status(time.Now(), 30, 7); st != store.StatusWarning {
			t.Fatalf("status = %s", st)
		}

		// Notification history survives deletion of the group.
		n1 := &store.Notification{GroupID: &g.ID, CertificateID: &c.ID, RecipientID: &r.ID, GroupName: g.Name,
			CertificateURL: c.URL, RecipientEmail: r.Email, NotAfter: na, ThresholdDays: 14, DaysLeft: 10}
		if ok, err := st.InsertNotification(ctx, n1); !ok || err != nil {
			t.Fatalf("insert notification: %v %v", ok, err)
		}
		dup := *n1
		if ok, err := st.InsertNotification(ctx, &dup); ok || err != nil {
			t.Fatalf("duplicate notification: %v %v", ok, err)
		}
		if err := st.DeleteGroup(ctx, g.ID); err != nil {
			t.Fatal(err)
		}
		hist, total, err := st.ListNotifications(ctx, store.NotificationFilter{})
		if err != nil || total != 1 || hist[0].GroupID != nil || hist[0].GroupName != "Παραγωγή" {
			t.Fatalf("history after group deletion: %+v %v", hist, err)
		}
		if ids, _ := st.CertificateGroupIDs(ctx); len(ids[c.ID]) != 0 {
			t.Fatal("membership survived group deletion")
		}
	})
}

func TestSettingsLocksAndMailLog(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		st := store.New(d)
		for _, v := range []string{`{"a":1}`, `{"a":2}`, `{"a":2}`} {
			if err := st.SetSetting(ctx, "x", v); err != nil {
				t.Fatal(err)
			}
		}
		if v, ok, _ := st.GetSetting(ctx, "x"); !ok || v != `{"a":2}` {
			t.Fatalf("setting = %q %v", v, ok)
		}

		// Leases: one owner at a time, renewable, taken over after expiry.
		if ok, err := st.TryLock(ctx, "scheduler", "a", time.Minute); !ok || err != nil {
			t.Fatalf("a: %v %v", ok, err)
		}
		if ok, _ := st.TryLock(ctx, "scheduler", "a", time.Minute); !ok {
			t.Fatal("renewal failed")
		}
		if ok, _ := st.TryLock(ctx, "scheduler", "b", time.Minute); ok {
			t.Fatal("b took a held lease")
		}
		st.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
		if ok, _ := st.TryLock(ctx, "scheduler", "b", time.Minute); !ok {
			t.Fatal("b could not take an expired lease")
		}
		st.Now = time.Now

		for i, rcpt := range []string{"a@example.com", "b@example.com", "c@example.com"} {
			e := &store.MailLogEntry{Kind: store.MailAlert, Recipient: rcpt, Subject: "s", Status: store.MailSent,
				GroupName: "G", Certificates: []string{"https://x.example.com"}, CertCount: 1}
			if i == 2 {
				e.Kind, e.Status, e.Error = store.MailInvite, store.MailFailed, "535 auth failed"
			}
			if err := st.AddMailLog(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		items, total, err := st.ListMailLog(ctx, store.MailLogFilter{Kinds: []string{store.MailAlert}, Query: "B@EXAMPLE"})
		if err != nil || total != 1 || items[0].Recipient != "b@example.com" || items[0].Certificates[0] != "https://x.example.com" {
			t.Fatalf("filter: %+v %v", items, err)
		}
		counts, err := st.MailLogCounts(ctx, time.Now().Add(-time.Hour), nil)
		if err != nil || counts[store.MailSent] != 2 || counts[store.MailFailed] != 1 {
			t.Fatalf("counts = %v %v", counts, err)
		}
	})
}
