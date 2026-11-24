package alerts_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
	"github.com/master2sid2/cert-alerter/internal/notify"
	"github.com/master2sid2/cert-alerter/internal/secret"
	"github.com/master2sid2/cert-alerter/internal/store"
	"github.com/master2sid2/cert-alerter/internal/testutil"
)

// fakeProvider records messages per channel ("to" field) instead of
// sending them.
type fakeProvider struct {
	mu   sync.Mutex
	sent map[string][]*notify.Message
	fail map[string]error
}

func (p *fakeProvider) Info() notify.Info {
	return notify.Info{Type: "fake", Label: "Fake", Target: []notify.Field{
		{Name: "to", Label: "To", Kind: notify.KindText, Required: true},
		{Name: "token", Label: "Token", Kind: notify.KindSecret},
	}}
}

func (p *fakeProvider) Send(_ context.Context, _ *notify.Client, t notify.Target, msg *notify.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	to := t.Config["to"]
	if err := p.fail[to]; err != nil {
		return err
	}
	p.sent[to] = append(p.sent[to], msg)
	return nil
}

func (p *fakeProvider) reset() {
	p.mu.Lock()
	p.sent, p.fail = map[string][]*notify.Message{}, map[string]error{}
	p.mu.Unlock()
}

// digest returns channel -> certificate names of each message.
func (p *fakeProvider) digest() map[string][][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string][][]string{}
	for to, msgs := range p.sent {
		for _, m := range msgs {
			var names []string
			for _, it := range m.Items {
				names = append(names, it.Name)
			}
			sort.Strings(names)
			out[to] = append(out[to], names)
		}
	}
	return out
}

var fake = &fakeProvider{}

func init() { notify.Register(fake) }

func (f *fixture) channels(t *testing.T) *notify.Service {
	t.Helper()
	box, _ := secret.NewBox(make([]byte, 32))
	svc := notify.NewService(f.st, box, notify.NewClient(nil, time.Second), slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.eng.Channels = svc
	return svc
}

func addChannel(t *testing.T, svc *notify.Service, name, to string, groups ...int64) int64 {
	t.Helper()
	c, err := svc.CreateChannel(context.Background(), notify.ChannelInput{Type: "fake", Name: name,
		Config: notify.Config{"to": to, "token": "s3cret"}, GroupIDs: &groups})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestChannelAlerts(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake.reset()
		f := newFixture(t, d, false) // channels do not depend on SMTP
		svc := f.channels(t)
		addChannel(t, svc, "Ops chat", "ops", f.groups["A"], f.groups["B"])
		broken := addChannel(t, svc, "Broken", "broken", f.groups["B"])
		fake.fail["broken"] = errors.New("HTTP 404: channel_not_found")

		res := f.run(t)
		// One message per group and channel; c4 (no group), c5 (paused) and
		// c6 (not due) are never mentioned.
		if got := fake.digest()["ops"]; len(got) != 2 || !slices.ContainsFunc(got, func(n []string) bool { return slices.Equal(n, []string{"c1", "c3"}) }) ||
			!slices.ContainsFunc(got, func(n []string) bool { return slices.Equal(n, []string{"c2", "c3"}) }) {
			t.Fatalf("ops chat got %v", got)
		}
		if len(f.sender.Messages()) != 0 {
			t.Fatal("e-mail sent without SMTP")
		}
		if res.Failed != 2 { // the broken channel: c2 and c3 of group B
			t.Fatalf("run: %+v", res)
		}
		msg := fake.sent["ops"][0]
		if msg.GroupURL == "" || msg.Items[0].Link == "" || msg.Kind != notify.KindAlert {
			t.Errorf("message without links: %+v", msg)
		}

		// The history shows channel notifications with their type.
		list, _, err := f.st.ListNotifications(ctx, store.NotificationFilter{Channel: "fake", Status: store.NotificationSent})
		if err != nil || len(list) != 4 || list[0].ChannelType != "fake" || list[0].RecipientEmail != "Ops chat" {
			t.Fatalf("history: %+v (%v)", list, err)
		}
		journal, _, err := f.st.ListMailLog(ctx, store.MailLogFilter{Channel: "fake", Status: store.MailFailed})
		if err != nil || len(journal) != 1 || journal[0].Recipient != "Broken" || journal[0].Error != "HTTP 404: channel_not_found" {
			t.Fatalf("journal: %+v (%v)", journal, err)
		}

		// Nothing is sent twice.
		fake.reset()
		fake.fail["broken"] = errors.New("still broken")
		if res := f.run(t); res.Created != 0 || len(fake.digest()) != 0 {
			t.Fatalf("second run: %+v %v", res, fake.digest())
		}

		// Disabling the broken channel cancels its queued alerts.
		off := false
		if _, err := svc.UpdateChannel(ctx, broken, notify.ChannelInput{Name: "Broken", Enabled: &off}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(backoffMax())
		if res := f.run(t); res.Cancelled != 2 {
			t.Fatalf("disabled channel: %+v", res)
		}

		// The secret stays stored (encrypted) when an update leaves it empty.
		c, err := f.st.GetChannel(ctx, broken)
		if err != nil || c.Config["token"] == "" || c.Config["token"] == "s3cret" {
			t.Fatalf("stored token %q (%v)", c.Config["token"], err)
		}
		v, err := svc.Channel(ctx, broken)
		if err != nil || v.Config["token"] != "" || !slices.Equal(v.SecretsSet, []string{"token"}) || v.Enabled {
			t.Fatalf("view %+v (%v)", v, err)
		}
	})
}

func TestDisabledMessengerIsSkipped(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake.reset()
		f := newFixture(t, d, false)
		svc := f.channels(t)
		addChannel(t, svc, "Ops chat", "ops", f.groups["A"])
		if err := svc.SaveProvider(ctx, "fake", notify.ProviderInput{Enabled: false}); err != nil {
			t.Fatal(err)
		}
		if res := f.run(t); len(fake.digest()) != 0 || res.Sent != 0 {
			t.Fatalf("disabled messenger delivered: %+v", res)
		}
		if err := svc.SaveProvider(ctx, "fake", notify.ProviderInput{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if res := f.run(t); len(fake.digest()["ops"]) != 1 || res.Sent != 2 {
			t.Fatalf("enabled messenger: %+v %v", res, fake.digest())
		}
	})
}

func TestGroupTestIncludesChannels(t *testing.T) {
	testutil.ForEachDB(t, func(t *testing.T, d *db.DB) {
		fake.reset()
		f := newFixture(t, d, false)
		svc := f.channels(t)
		addChannel(t, svc, "Ops chat", "ops", f.groups["A"])
		res, err := f.eng.SendGroupTest(context.Background(), f.groups["A"])
		if err != nil {
			t.Fatal(err)
		}
		// Two recipients without SMTP (reported) and the channel (sent).
		var ok, failed int
		for _, r := range res {
			if r.Error == "" {
				ok++
			} else {
				failed++
			}
		}
		if ok != 1 || failed != 2 || fake.sent["ops"][0].Kind != notify.KindTest || len(fake.sent["ops"][0].Items) != 4 {
			t.Fatalf("results %+v", res)
		}
	})
}

func backoffMax() time.Duration { return 6 * time.Hour }
