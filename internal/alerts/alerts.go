// Package alerts decides which expiry notifications are due and delivers
// them by e-mail, strictly per group: recipients only ever receive alerts
// about certificates of groups they belong to.
//
// Delivery uses an outbox: Evaluate creates one pending notification per
// (group, certificate, recipient, certificate expiry, threshold) — a unique
// key, so each stage is sent at most once — and Dispatch delivers pending
// notifications as one digest e-mail per group and recipient, retrying
// failures with backoff.
package alerts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/master2sid2/cert-alerter/internal/mailer"
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
	if !cfg.SMTP.Configured() {
		return res, nil // keep notifications pending until e-mail is configured
	}
	if err := e.dispatch(ctx, cfg, now, &res); err != nil {
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

type outgoing struct {
	msg      *mailer.Message
	ids      []int64
	attempts int
	group    string
}

func (e *Engine) dispatch(ctx context.Context, cfg settings.All, now time.Time, res *Result) error {
	due, err := e.store.ListDueNotifications(ctx, now, 5000)
	if err != nil || len(due) == 0 {
		return err
	}

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
		// Several stages of one certificate may be pending (e.g. SMTP was
		// down for a while): only the most urgent one is sent.
		best := map[int64]store.DueNotification{}
		var superseded []int64
		for _, d := range batches[k] {
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

// RecipientResult reports delivery of a test message.
type RecipientResult struct {
	Email string `json:"email"`
	Error string `json:"error,omitempty"`
}

// ErrNoRecipients is returned when a group has nobody to notify.
var ErrNoRecipients = errors.New("group has no recipients")

// SendGroupTest sends a test notification to every recipient of a group.
func (e *Engine) SendGroupTest(ctx context.Context, groupID int64) ([]RecipientResult, error) {
	cfg, err := e.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.SMTP.Configured() {
		return nil, mailer.ErrNotConfigured
	}
	g, err := e.store.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	recipients, err := e.store.GroupRecipients(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	certIDs, err := e.store.GroupCertificateIDs(ctx, groupID)
	if err != nil {
		return nil, err
	}
	now := e.Now().UTC()
	var items []mailer.AlertItem
	for _, id := range certIDs {
		c, err := e.store.GetCertificate(ctx, id)
		if err != nil {
			continue
		}
		it := mailer.AlertItem{CertificateID: c.ID, Name: c.Name, URL: c.URL, SubjectCN: c.SubjectCN, IssuerCN: c.IssuerCN}
		if c.NotAfter != nil {
			it.NotAfter = *c.NotAfter
			it.DaysLeft = store.DaysUntil(*c.NotAfter, now)
		}
		items = append(items, it)
	}
	slices.SortFunc(items, func(a, b mailer.AlertItem) int {
		if a.NotAfter.IsZero() != b.NotAfter.IsZero() {
			if a.NotAfter.IsZero() {
				return 1
			}
			return -1
		}
		return cmp.Or(a.NotAfter.Compare(b.NotAfter), cmp.Compare(a.URL, b.URL))
	})
	if len(items) > 50 {
		items = items[:50]
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
		out[i] = RecipientResult{Email: m.To}
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
