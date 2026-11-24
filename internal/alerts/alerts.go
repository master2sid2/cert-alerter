// Package alerts decides which expiry notifications are due and delivers
// them by e-mail and to messenger channels, strictly per group: recipients
// and channels only ever receive alerts about certificates of their groups.
//
// Delivery uses an outbox: Evaluate creates one pending notification per
// (group, certificate, recipient or channel, certificate expiry,
// threshold) — a unique key, so each stage is sent at most once — and
// Dispatch delivers pending notifications as one digest per group and
// recipient (e-mail) or group and channel (messenger), retrying failures
// with backoff.
package alerts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/master2sid2/cert-alerter/internal/mailer"
	"github.com/master2sid2/cert-alerter/internal/notify"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
)

const (
	// ExpiredLookback limits how long after expiry an "expired" alert is
	// still created for a certificate.
	ExpiredLookback = 90 * 24 * time.Hour
	// MaxAttempts is the number of delivery attempts before giving up.
	MaxAttempts = 12
	// ExpiredThreshold is the threshold value used for "already expired".
	ExpiredThreshold = 0

	batchSize = 50
)

// Engine evaluates and dispatches alerts.
type Engine struct {
	store    *store.Store
	settings *settings.Service
	sender   mailer.Sender
	log      *slog.Logger
	// Channels delivers to messengers; nil disables channel alerts.
	Channels *notify.Service
	// Now is the clock; replaceable in tests.
	Now func() time.Time

	mu sync.Mutex
}

// New creates an Engine.
func New(st *store.Store, set *settings.Service, sender mailer.Sender, log *slog.Logger) *Engine {
	return &Engine{store: st, settings: set, sender: sender, log: log, Now: time.Now}
}

// Stage returns the alert threshold that applies to a certificate expiring
// at notAfter: the smallest configured threshold that is >= the number of
// whole days left, or ExpiredThreshold once expired. ok is false when no
// alert applies yet.
func Stage(notAfter, now time.Time, thresholds []int, notifyExpired bool) (threshold int, ok bool) {
	if !now.Before(notAfter) {
		return ExpiredThreshold, notifyExpired
	}
	days := store.DaysUntil(notAfter, now)
	best := -1
	for _, t := range thresholds {
		if t >= days && (best == -1 || t < best) {
			best = t
		}
	}
	return best, best > 0
}

// Result summarises one Run.
type Result struct {
	Created   int `json:"created"`
	Sent      int `json:"sent"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Run evaluates and dispatches alerts once.
func (e *Engine) Run(ctx context.Context) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := e.settings.Get(ctx)
	if err != nil {
		return Result{}, err
	}
	now := e.Now().UTC()
	var res Result
	res.Created, err = e.evaluate(ctx, cfg, now)
	if err != nil {
		return res, fmt.Errorf("evaluate alerts: %w", err)
	}
	var available map[string]bool
	if e.Channels != nil {
		if available, err = e.Channels.Available(ctx); err != nil {
			return res, err
		}
		n, err := e.evaluateChannels(ctx, cfg, now, available)
		res.Created += n
		if err != nil {
			return res, fmt.Errorf("evaluate channel alerts: %w", err)
		}
	}
	if err := e.dispatch(ctx, cfg, now, available, &res); err != nil {
		return res, fmt.Errorf("dispatch alerts: %w", err)
	}
	return res, nil
}

func (e *Engine) evaluate(ctx context.Context, cfg settings.All, now time.Time) (int, error) {
	m := cfg.Monitoring
	if len(m.Thresholds) == 0 {
		return 0, nil
	}
	from := now.Add(-ExpiredLookback)
	to := now.Add(time.Duration(slices.Max(m.Thresholds)+1) * 24 * time.Hour)
	cands, err := e.store.ListAlertCandidates(ctx, from, to)
	if err != nil || len(cands) == 0 {
		return 0, err
	}
	existing, err := e.store.NotificationKeys(ctx, from, to)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, c := range cands {
		stage, ok := Stage(c.NotAfter, now, m.Thresholds, m.NotifyExpired)
		if !ok {
			continue
		}
		key := store.NotificationKey{GroupID: c.GroupID, CertificateID: c.CertificateID, RecipientID: c.RecipientID,
			NotAfter: c.NotAfter.Unix(), Threshold: stage}
		if _, seen := existing[key]; seen {
			continue
		}
		inserted, err := e.store.InsertNotification(ctx, &store.Notification{
			GroupID: new(c.GroupID), CertificateID: new(c.CertificateID), RecipientID: new(c.RecipientID),
			GroupName: c.GroupName, CertificateURL: c.CertificateURL, RecipientEmail: c.RecipientEmail,
			NotAfter: c.NotAfter, ThresholdDays: stage, DaysLeft: store.DaysUntil(c.NotAfter, now),
		})
		if err != nil {
			return created, err
		}
		existing[key] = struct{}{}
		if inserted {
			created++
		}
	}
	return created, nil
}

func (e *Engine) evaluateChannels(ctx context.Context, cfg settings.All, now time.Time, available map[string]bool) (int, error) {
	m := cfg.Monitoring
	if len(m.Thresholds) == 0 {
		return 0, nil
	}
	from := now.Add(-ExpiredLookback)
	to := now.Add(time.Duration(slices.Max(m.Thresholds)+1) * 24 * time.Hour)
	cands, err := e.store.ListChannelAlertCandidates(ctx, from, to)
	if err != nil || len(cands) == 0 {
		return 0, err
	}
	existing, err := e.store.ChannelNotificationKeys(ctx, from, to)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, c := range cands {
		if !available[c.ChannelType] {
			continue
		}
		stage, ok := Stage(c.NotAfter, now, m.Thresholds, m.NotifyExpired)
		if !ok {
			continue
		}
		key := store.ChannelNotificationKey{GroupID: c.GroupID, CertificateID: c.CertificateID, ChannelID: c.ChannelID,
			NotAfter: c.NotAfter.Unix(), Threshold: stage}
		if _, seen := existing[key]; seen {
			continue
		}
		inserted, err := e.store.InsertNotification(ctx, &store.Notification{
			GroupID: new(c.GroupID), CertificateID: new(c.CertificateID), ChannelID: new(c.ChannelID),
			ChannelType: c.ChannelType, GroupName: c.GroupName, CertificateURL: c.CertificateURL,
			RecipientEmail: c.ChannelName, NotAfter: c.NotAfter, ThresholdDays: stage,
			DaysLeft: store.DaysUntil(c.NotAfter, now),
		})
		if err != nil {
			return created, err
		}
		existing[key] = struct{}{}
		if inserted {
			created++
		}
	}
	return created, nil
}

// mostUrgent keeps one notification per certificate: several stages of a
// certificate may be pending (e.g. delivery was down for a while), only
// the most urgent one is sent. It returns the kept notifications and the
// ids of the superseded ones.
func mostUrgent(batch []store.DueNotification) ([]store.DueNotification, []int64) {
	best := map[int64]store.DueNotification{}
	var superseded []int64
	for _, d := range batch {
		cid := *d.CertificateID
		cur, ok := best[cid]
		switch {
		case !ok:
			best[cid] = d
		case d.ThresholdDays < cur.ThresholdDays:
			superseded = append(superseded, cur.ID)
			best[cid] = d
		default:
			superseded = append(superseded, d.ID)
		}
	}
	kept := make([]store.DueNotification, 0, len(best))
	for _, d := range best {
		kept = append(kept, d)
	}
	slices.SortFunc(kept, func(a, b store.DueNotification) int {
		return cmp.Or(a.CurrentNotAfter.Compare(*b.CurrentNotAfter), cmp.Compare(a.CertificateURL, b.CertificateURL))
	})
	return kept, superseded
}

type outgoing struct {
	msg      *mailer.Message
	ids      []int64
	attempts int
	group    string
}

func (e *Engine) dispatch(ctx context.Context, cfg settings.All, now time.Time, available map[string]bool, res *Result) error {
	due, err := e.store.ListDueNotifications(ctx, now, 5000)
	if err != nil || len(due) == 0 {
		return err
	}
	var mail, channel []store.DueNotification
	for _, d := range due {
		if d.ChannelType == "" {
			mail = append(mail, d)
		} else {
			channel = append(channel, d)
		}
	}
	// E-mail notifications stay pending until SMTP is configured.
	if len(mail) > 0 && cfg.SMTP.Configured() {
		if err := e.dispatchMail(ctx, cfg, now, mail, res); err != nil {
			return err
		}
	}
	if len(channel) > 0 && e.Channels != nil {
		if err := e.dispatchChannels(ctx, cfg, now, channel, available, res); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) dispatchMail(ctx context.Context, cfg settings.All, now time.Time, due []store.DueNotification, res *Result) error {
	type batchKey struct{ group, recipient int64 }
	var (
		stale   []int64
		order   []batchKey
		batches = map[batchKey][]store.DueNotification{}
	)
	for _, d := range due {
		if !d.Valid {
			stale = append(stale, d.ID)
			continue
		}
		k := batchKey{*d.GroupID, *d.RecipientID}
		if _, ok := batches[k]; !ok {
			order = append(order, k)
		}
		batches[k] = append(batches[k], d)
	}
	if err := e.store.CancelNotifications(ctx, stale, "certificate, group membership or recipient changed"); err != nil {
		return err
	}
	res.Cancelled += len(stale)

	brand := Branding(cfg)
	var mails []outgoing
	for _, k := range order {
		best, superseded := mostUrgent(batches[k])
		if err := e.store.CancelNotifications(ctx, superseded, "superseded by a more urgent alert"); err != nil {
			return err
		}
		res.Cancelled += len(superseded)

		var (
			items    []mailer.AlertItem
			ids      []int64
			attempts int
			first    store.DueNotification
		)
		for _, d := range best {
			first = d
			items = append(items, mailer.AlertItem{
				CertificateID: *d.CertificateID, Name: d.CertificateName, URL: d.CertificateURL,
				SubjectCN: d.SubjectCN, IssuerCN: d.IssuerCN, NotAfter: *d.CurrentNotAfter,
				DaysLeft: store.DaysUntil(*d.CurrentNotAfter, now),
			})
			ids = append(ids, d.ID)
			attempts = max(attempts, d.Attempts)
		}
		slices.SortFunc(items, func(a, b mailer.AlertItem) int {
			return cmp.Or(a.NotAfter.Compare(b.NotAfter), cmp.Compare(a.URL, b.URL))
		})
		msg, err := brand.Alert(mailer.AlertParams{
			GroupID: k.group, GroupName: first.GroupName, RecipientMail: first.RecipientEmail, RecipientName: first.RecipientName,
			CriticalDays: cfg.Monitoring.CriticalDays, Items: items,
		})
		if err != nil {
			return err
		}
		mails = append(mails, outgoing{msg: msg, ids: ids, attempts: attempts, group: first.GroupName})
	}

	for chunk := range slices.Chunk(mails, batchSize) {
		msgs := make([]*mailer.Message, len(chunk))
		for i, m := range chunk {
			msgs[i] = m.msg
		}
		errs := e.sender.Send(ctx, cfg.SMTP, msgs)
		for i, m := range chunk {
			if err := errs[i]; err != nil {
				attempts := m.attempts + 1
				final := attempts >= MaxAttempts
				next := now.Add(Backoff(attempts))
				if dbErr := e.store.MarkNotificationsFailed(ctx, m.ids, err.Error(), next, final); dbErr != nil {
					return dbErr
				}
				res.Failed += len(m.ids)
				e.log.Warn("alert delivery failed", "to", m.msg.To, "group", m.group,
					"attempt", attempts, "final", final, "err", err)
				continue
			}
			if err := e.store.MarkNotificationsSent(ctx, m.ids, now); err != nil {
				return err
			}
			res.Sent += len(m.ids)
			e.log.Info("alert sent", "to", m.msg.To, "group", m.group, "certificates", len(m.ids))
		}
	}
	return nil
}

func (e *Engine) dispatchChannels(ctx context.Context, cfg settings.All, now time.Time, due []store.DueNotification,
	available map[string]bool, res *Result) error {
	list, err := e.store.ListChannels(ctx)
	if err != nil {
		return err
	}
	channels := make(map[int64]*store.Channel, len(list))
	for i := range list {
		channels[list[i].ID] = &list[i]
	}
	type batchKey struct{ group, channel int64 }
	var (
		stale, off []int64
		order      []batchKey
		batches    = map[batchKey][]store.DueNotification{}
	)
	for _, d := range due {
		switch {
		case !d.Valid || channels[*d.ChannelID] == nil:
			stale = append(stale, d.ID)
			continue
		case !available[channels[*d.ChannelID].Type]:
			off = append(off, d.ID)
			continue
		}
		k := batchKey{*d.GroupID, *d.ChannelID}
		if _, ok := batches[k]; !ok {
			order = append(order, k)
		}
		batches[k] = append(batches[k], d)
	}
	if err := e.store.CancelNotifications(ctx, stale, "certificate, group or channel changed"); err != nil {
		return err
	}
	if err := e.store.CancelNotifications(ctx, off, "messenger disabled or not configured"); err != nil {
		return err
	}
	res.Cancelled += len(stale) + len(off)

	for _, k := range order {
		best, superseded := mostUrgent(batches[k])
		if err := e.store.CancelNotifications(ctx, superseded, "superseded by a more urgent alert"); err != nil {
			return err
		}
		res.Cancelled += len(superseded)
		first := best[0]
		msg := e.channelMessage(cfg, notify.KindAlert, k.group, first.GroupName)
		var (
			ids      []int64
			attempts int
		)
		for _, d := range best {
			msg.Items = append(msg.Items, e.item(cfg, *d.CertificateID, d.CertificateName, d.CertificateURL,
				d.SubjectCN, d.IssuerCN, d.CurrentNotAfter, now))
			ids = append(ids, d.ID)
			attempts = max(attempts, d.Attempts)
		}
		ch := channels[k.channel]
		err := e.Channels.Send(ctx, ch, msg, notify.Delivery{Kind: store.MailAlert, GroupID: k.group, GroupName: first.GroupName})
		if err != nil {
			attempts++
			final := attempts >= MaxAttempts
			if dbErr := e.store.MarkNotificationsFailed(ctx, ids, err.Error(), now.Add(Backoff(attempts)), final); dbErr != nil {
				return dbErr
			}
			res.Failed += len(ids)
			e.log.Warn("alert delivery failed", "channel", ch.Name, "type", ch.Type, "group", first.GroupName,
				"attempt", attempts, "final", final, "err", err)
			continue
		}
		if err := e.store.MarkNotificationsSent(ctx, ids, now); err != nil {
			return err
		}
		res.Sent += len(ids)
		e.log.Info("alert sent", "channel", ch.Name, "type", ch.Type, "group", first.GroupName, "certificates", len(ids))
	}
	return nil
}

// channelMessage starts a messenger message about a group.
func (e *Engine) channelMessage(cfg settings.All, kind string, groupID int64, groupName string) *notify.Message {
	m := &notify.Message{
		Kind: kind, AppName: cfg.General.AppName, AppURL: link(cfg, "/"), GroupName: groupName,
		CriticalDays: cfg.Monitoring.CriticalDays, Location: cfg.General.Location(),
	}
	if groupID > 0 {
		m.GroupURL = link(cfg, "/groups/"+strconv.FormatInt(groupID, 10))
	}
	return m
}

func (e *Engine) item(cfg settings.All, certID int64, name, url, subjectCN, issuerCN string, notAfter *time.Time, now time.Time) notify.Item {
	it := notify.Item{Name: name, URL: url, SubjectCN: subjectCN, IssuerCN: issuerCN,
		Link: link(cfg, "/certificates/"+strconv.FormatInt(certID, 10))}
	if notAfter != nil {
		it.NotAfter = *notAfter
		it.DaysLeft = store.DaysUntil(*notAfter, now)
	}
	return it
}

// link returns an absolute URL of the web interface ("" without base URL).
func link(cfg settings.All, path string) string {
	if cfg.General.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(cfg.General.BaseURL, "/") + path
}

// TestMessage returns the sample message used to test a channel.
func (e *Engine) TestMessage(ctx context.Context) (*notify.Message, error) {
	cfg, err := e.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	return e.channelMessage(cfg, notify.KindChannelTest, 0, ""), nil
}

// Backoff returns the delay before the next delivery attempt.
func Backoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 5 * time.Minute
	case attempts == 2:
		return 15 * time.Minute
	case attempts == 3:
		return time.Hour
	case attempts == 4:
		return 3 * time.Hour
	}
	return 6 * time.Hour
}

// RecipientResult reports delivery of a test message to one recipient or
// channel.
type RecipientResult struct {
	Channel string `json:"channel"` // "email" or the channel type
	Target  string `json:"target"`  // e-mail address or channel name
	Error   string `json:"error,omitempty"`
}

// ErrNoRecipients is returned when a group has nobody to notify.
var ErrNoRecipients = errors.New("group has no recipients and no channels")

// SendGroupTest sends a test notification to every recipient and channel
// of a group.
func (e *Engine) SendGroupTest(ctx context.Context, groupID int64) ([]RecipientResult, error) {
	cfg, err := e.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	g, err := e.store.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	recipients, err := e.store.GroupRecipients(ctx, groupID)
	if err != nil {
		return nil, err
	}
	var channels []store.Channel
	if e.Channels != nil {
		if channels, err = e.store.GroupChannels(ctx, groupID); err != nil {
			return nil, err
		}
	}
	if len(recipients) == 0 && len(channels) == 0 {
		return nil, ErrNoRecipients
	}
	if len(recipients) > 0 && len(channels) == 0 && !cfg.SMTP.Configured() {
		return nil, mailer.ErrNotConfigured
	}
	certIDs, err := e.store.GroupCertificateIDs(ctx, groupID)
	if err != nil {
		return nil, err
	}
	now := e.Now().UTC()
	var certs []*store.Certificate
	for _, id := range certIDs {
		if c, err := e.store.GetCertificate(ctx, id); err == nil {
			certs = append(certs, c)
		}
	}
	slices.SortFunc(certs, func(a, b *store.Certificate) int {
		if (a.NotAfter == nil) != (b.NotAfter == nil) {
			if a.NotAfter == nil {
				return 1
			}
			return -1
		}
		if a.NotAfter != nil {
			if c := a.NotAfter.Compare(*b.NotAfter); c != 0 {
				return c
			}
		}
		return cmp.Compare(a.URL, b.URL)
	})
	if len(certs) > 50 {
		certs = certs[:50]
	}

	var out []RecipientResult
	if len(recipients) > 0 {
		if cfg.SMTP.Configured() {
			res, err := e.sendGroupTestMail(ctx, cfg, g, recipients, certs, now)
			if err != nil {
				return nil, err
			}
			out = append(out, res...)
		} else {
			for _, r := range recipients {
				out = append(out, RecipientResult{Channel: store.ChannelEmail, Target: r.Email, Error: mailer.ErrNotConfigured.Error()})
			}
		}
	}
	if len(channels) > 0 {
		available, err := e.Channels.Available(ctx)
		if err != nil {
			return nil, err
		}
		for i := range channels {
			ch := &channels[i]
			r := RecipientResult{Channel: ch.Type, Target: ch.Name}
			switch {
			case !ch.Enabled:
				r.Error = "the channel is disabled"
			case !available[ch.Type]:
				r.Error = "the messenger is disabled or not configured"
			default:
				msg := e.channelMessage(cfg, notify.KindTest, g.ID, g.Name)
				msg.Thresholds = cfg.Monitoring.Thresholds
				for _, c := range certs {
					msg.Items = append(msg.Items, e.item(cfg, c.ID, c.Name, c.URL, c.SubjectCN, c.IssuerCN, c.NotAfter, now))
				}
				if err := e.Channels.Send(ctx, ch, msg, notify.Delivery{Kind: store.MailGroupTest, GroupID: g.ID, GroupName: g.Name}); err != nil {
					r.Error = err.Error()
				}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (e *Engine) sendGroupTestMail(ctx context.Context, cfg settings.All, g *store.Group, recipients []store.Recipient,
	certs []*store.Certificate, now time.Time) ([]RecipientResult, error) {
	items := make([]mailer.AlertItem, 0, len(certs))
	for _, c := range certs {
		it := mailer.AlertItem{CertificateID: c.ID, Name: c.Name, URL: c.URL, SubjectCN: c.SubjectCN, IssuerCN: c.IssuerCN}
		if c.NotAfter != nil {
			it.NotAfter = *c.NotAfter
			it.DaysLeft = store.DaysUntil(*c.NotAfter, now)
		}
		items = append(items, it)
	}
	brand := Branding(cfg)
	msgs := make([]*mailer.Message, 0, len(recipients))
	for _, r := range recipients {
		m, err := brand.GroupTest(g.ID, g.Name, r.Email, r.Name, cfg.Monitoring.Thresholds, items, cfg.Monitoring.CriticalDays)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	errs := e.sender.Send(ctx, cfg.SMTP, msgs)
	out := make([]RecipientResult, len(msgs))
	for i, m := range msgs {
		out[i] = RecipientResult{Channel: store.ChannelEmail, Target: m.To}
		if errs[i] != nil {
			out[i].Error = errs[i].Error()
		}
	}
	return out, nil
}

// Branding derives the e-mail branding from settings.
func Branding(cfg settings.All) mailer.Branding {
	return mailer.Branding{
		AppName:  cfg.General.AppName,
		BaseURL:  cfg.General.BaseURL,
		Language: cfg.General.EmailLanguage,
		Location: cfg.General.Location(),
	}
}
