// Package monitor runs certificate checks (on demand and on a schedule) and
// drives the alert engine. When several instances share one database, a
// lease in the database elects a single instance that runs scheduled jobs.
package monitor

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/master2sid2/cert-alerter/internal/alerts"
	"github.com/master2sid2/cert-alerter/internal/checker"
	"github.com/master2sid2/cert-alerter/internal/settings"
	"github.com/master2sid2/cert-alerter/internal/store"
)

const (
	leaseName         = "scheduler"
	leaseTTL          = 90 * time.Second
	leaseRenew        = 30 * time.Second
	tickInterval      = 5 * time.Second
	alertInterval     = time.Minute
	housekeepInterval = time.Hour
	stateLastFull     = "state.last_full_check"
)

// Monitor coordinates checks and alerts.
type Monitor struct {
	store      *store.Store
	settings   *settings.Service
	alerts     *alerts.Engine
	log        *slog.Logger
	instanceID string
	scheduler  bool
	// Roots overrides the trust store used for verification (tests).
	Roots *x509.CertPool

	ctx context.Context // lifetime of background work

	leader      atomic.Bool
	alertsDue   atomic.Bool
	fullRunning atomic.Bool
	progress    struct{ done, total atomic.Int64 }

	mu           sync.Mutex
	lastFull     *time.Time
	lastAlertRun *time.Time
	lastAlertErr string
	lastAlertRes alerts.Result
	inflight     map[int64]bool
	wg           sync.WaitGroup
}

// New creates a Monitor. ctx bounds background work started on demand.
// When scheduler is false the instance never runs scheduled jobs.
func New(ctx context.Context, st *store.Store, set *settings.Service, eng *alerts.Engine, log *slog.Logger,
	instanceID string, scheduler bool) *Monitor {
	return &Monitor{
		store: st, settings: set, alerts: eng, log: log, instanceID: instanceID, scheduler: scheduler,
		ctx: ctx, inflight: map[int64]bool{},
	}
}

// Status describes scheduler state for the dashboard.
type Status struct {
	Scheduler      bool          `json:"scheduler"`
	Leader         bool          `json:"leader"`
	CheckRunning   bool          `json:"check_running"`
	CheckDone      int64         `json:"check_done"`
	CheckTotal     int64         `json:"check_total"`
	LastFullCheck  *time.Time    `json:"last_full_check"`
	NextFullCheck  *time.Time    `json:"next_full_check"`
	LastAlertRun   *time.Time    `json:"last_alert_run"`
	LastAlertError string        `json:"last_alert_error"`
	LastAlert      alerts.Result `json:"last_alert_result"`
}

// Status returns a snapshot of the scheduler state.
func (m *Monitor) Status(ctx context.Context) Status {
	m.mu.Lock()
	st := Status{
		Scheduler: m.scheduler, Leader: m.leader.Load(), CheckRunning: m.fullRunning.Load(),
		CheckDone: m.progress.done.Load(), CheckTotal: m.progress.total.Load(),
		LastFullCheck: m.lastFull, LastAlertRun: m.lastAlertRun, LastAlertError: m.lastAlertErr, LastAlert: m.lastAlertRes,
	}
	m.mu.Unlock()
	if cfg, err := m.settings.Get(ctx); err == nil && st.Scheduler {
		next := time.Now().UTC()
		if st.LastFullCheck != nil {
			next = st.LastFullCheck.Add(time.Duration(cfg.Monitoring.CheckIntervalMinutes) * time.Minute)
		}
		st.NextFullCheck = &next
	}
	return st
}

// KickAlerts asks the scheduler to evaluate alerts soon.
func (m *Monitor) KickAlerts() { m.alertsDue.Store(true) }

// CheckCertificate checks one certificate synchronously, stores the result
// and returns the updated certificate.
func (m *Monitor) CheckCertificate(ctx context.Context, id int64) (*store.Certificate, error) {
	c, err := m.store.GetCertificate(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, err := m.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	// Interactive checks are not retried so the user gets a quick answer.
	o := m.check(ctx, cfg, store.CheckTarget{ID: c.ID, Host: c.Host, Port: c.Port}, 0)
	if err := m.store.SaveCheckOutcome(ctx, c.ID, o); err != nil {
		return nil, err
	}
	m.KickAlerts()
	return m.store.GetCertificate(ctx, id)
}

// CheckAsync checks the given certificates in the background.
func (m *Monitor) CheckAsync(ids []int64) {
	if len(ids) == 0 {
		return
	}
	m.wg.Go(func() {
		cfg, err := m.settings.Get(m.ctx)
		if err != nil {
			m.log.Error("load settings", "err", err)
			return
		}
		targets := make([]store.CheckTarget, 0, len(ids))
		for _, id := range ids {
			c, err := m.store.GetCertificate(m.ctx, id)
			if err != nil {
				continue
			}
			targets = append(targets, store.CheckTarget{ID: c.ID, Host: c.Host, Port: c.Port})
		}
		m.checkTargets(m.ctx, cfg, targets, nil)
		m.KickAlerts()
	})
}

// StartFullCheck runs a check of all enabled certificates in the
// background. It returns false when a full check is already running.
func (m *Monitor) StartFullCheck() bool {
	if !m.fullRunning.CompareAndSwap(false, true) {
		return false
	}
	m.wg.Go(func() {
		defer m.fullRunning.Store(false)
		m.runFullCheck(m.ctx)
	})
	return true
}

// Wait blocks until background work has finished (after ctx cancellation).
func (m *Monitor) Wait() { m.wg.Wait() }

func (m *Monitor) runFullCheck(ctx context.Context) {
	cfg, err := m.settings.Get(ctx)
	if err != nil {
		m.log.Error("load settings", "err", err)
		return
	}
	targets, err := m.store.ListCheckTargets(ctx, false)
	if err != nil {
		m.log.Error("list certificates", "err", err)
		return
	}
	start := time.Now()
	m.progress.total.Store(int64(len(targets)))
	m.progress.done.Store(0)
	failed := m.checkTargets(ctx, cfg, targets, &m.progress.done)
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	m.mu.Lock()
	m.lastFull = &now
	m.mu.Unlock()
	if err := m.store.SetSetting(ctx, stateLastFull, strconv.FormatInt(now.Unix(), 10)); err != nil {
		m.log.Error("store last check time", "err", err)
	}
	m.log.Info("certificate check finished", "checked", len(targets), "failed", failed,
		"duration", time.Since(start).Round(time.Millisecond))
	m.KickAlerts()
}

// checkTargets checks targets with bounded concurrency and returns the
// number of failed checks.
func (m *Monitor) checkTargets(ctx context.Context, cfg settings.All, targets []store.CheckTarget, done *atomic.Int64) int {
	sem := make(chan struct{}, max(cfg.Monitoring.Concurrency, 1))
	var (
		wg     sync.WaitGroup
		failed atomic.Int64
	)
	for _, t := range targets {
		if !m.claim(t.ID) {
			continue // already being checked
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			m.release(t.ID)
			wg.Wait()
			return int(failed.Load())
		}
		wg.Go(func() {
			defer func() { <-sem; m.release(t.ID) }()
			o := m.check(ctx, cfg, t, 1)
			if o.Error != "" {
				failed.Add(1)
				m.log.Debug("check failed", "host", t.Host, "port", t.Port, "err", o.Error)
			}
			if ctx.Err() != nil {
				return
			}
			if err := m.store.SaveCheckOutcome(ctx, t.ID, o); err != nil && !errors.Is(err, store.ErrNotFound) {
				m.log.Error("save check result", "id", t.ID, "err", err)
			}
			if done != nil {
				done.Add(1)
			}
		})
	}
	wg.Wait()
	return int(failed.Load())
}

func (m *Monitor) claim(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[id] {
		return false
	}
	m.inflight[id] = true
	return true
}

func (m *Monitor) release(id int64) {
	m.mu.Lock()
	delete(m.inflight, id)
	m.mu.Unlock()
}

func (m *Monitor) check(ctx context.Context, cfg settings.All, t store.CheckTarget, retries int) store.CheckOutcome {
	ch := &checker.Checker{
		Timeout: time.Duration(cfg.Monitoring.CheckTimeoutSeconds) * time.Second,
		Roots:   m.Roots,
		Retries: retries,
	}
	res, err := ch.Check(ctx, checker.Target{Host: t.Host, Port: t.Port})
	now := time.Now().UTC()
	if err != nil {
		return store.CheckOutcome{CheckedAt: now, Error: err.Error()}
	}
	return ToOutcome(res, now)
}

// ToOutcome converts a checker result to the persisted representation.
func ToOutcome(r *checker.Result, at time.Time) store.CheckOutcome {
	leaf := r.Leaf
	alg, bits := checker.KeyInfo(leaf)
	o := store.CheckOutcome{
		CheckedAt:          at,
		SubjectCN:          checker.CommonName(leaf.Subject),
		SubjectOrg:         checker.Org(leaf.Subject),
		IssuerCN:           checker.CommonName(leaf.Issuer),
		IssuerOrg:          checker.Org(leaf.Issuer),
		SANs:               checker.SANs(leaf),
		SerialNumber:       checker.SerialHex(leaf),
		FingerprintSHA256:  checker.Fingerprint(leaf),
		SignatureAlgorithm: leaf.SignatureAlgorithm.String(),
		KeyAlgorithm:       alg,
		KeyBits:            bits,
		NotBefore:          leaf.NotBefore.UTC(),
		NotAfter:           leaf.NotAfter.UTC(),
		Trusted:            r.Trusted,
		VerifyError:        r.VerifyError,
		TLSVersion:         r.TLSVersion,
		ResolvedIP:         r.ResolvedIP,
	}
	for _, c := range r.Chain {
		o.Chain = append(o.Chain, store.ChainCert{
			Subject: checker.CommonName(c.Subject), Issuer: checker.CommonName(c.Issuer),
			NotBefore: c.NotBefore.UTC(), NotAfter: c.NotAfter.UTC(), IsCA: c.IsCA,
			FingerprintSHA256: checker.Fingerprint(c),
		})
	}
	return o
}

// Run executes scheduled jobs until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	if !m.scheduler {
		m.log.Info("scheduler disabled on this instance")
		<-ctx.Done()
		return
	}
	if v, ok, err := m.store.GetSetting(ctx, stateLastFull); err == nil && ok {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			t := time.Unix(sec, 0).UTC()
			m.mu.Lock()
			m.lastFull = &t
			m.mu.Unlock()
		}
	}
	started := time.Now()
	var lastLease, lastAlerts, lastHousekeep, lastPending time.Time
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	defer func() {
		if m.leader.Load() {
			_ = m.store.Unlock(context.WithoutCancel(ctx), leaseName, m.instanceID)
		}
	}()
	for {
		now := time.Now()
		if now.Sub(lastLease) >= leaseRenew {
			lastLease = now
			ok, err := m.store.TryLock(ctx, leaseName, m.instanceID, leaseTTL)
			if err != nil && ctx.Err() == nil {
				m.log.Error("scheduler lease", "err", err)
			}
			if ok != m.leader.Load() {
				m.leader.Store(ok)
				if ok {
					m.log.Info("this instance runs scheduled jobs", "instance", m.instanceID)
				} else {
					m.log.Info("another instance runs scheduled jobs")
				}
			}
		}
		if m.leader.Load() {
			cfg, err := m.settings.Get(ctx)
			if err == nil {
				// Give the HTTP server a few seconds before the first run.
				if now.Sub(started) > 10*time.Second && m.fullCheckDue(cfg, now) {
					m.StartFullCheck()
				}
				if now.Sub(lastPending) >= time.Minute && !m.fullRunning.Load() {
					lastPending = now
					if pending, err := m.store.ListCheckTargets(ctx, true); err == nil && len(pending) > 0 {
						ids := make([]int64, len(pending))
						for i, p := range pending {
							ids[i] = p.ID
						}
						m.CheckAsync(ids)
					}
				}
			}
			if m.alertsDue.Swap(false) || now.Sub(lastAlerts) >= alertInterval {
				lastAlerts = now
				m.runAlerts(ctx)
			}
			if now.Sub(lastHousekeep) >= housekeepInterval {
				lastHousekeep = now
				m.housekeeping(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Monitor) fullCheckDue(cfg settings.All, now time.Time) bool {
	if m.fullRunning.Load() {
		return false
	}
	m.mu.Lock()
	last := m.lastFull
	m.mu.Unlock()
	if last == nil {
		return true
	}
	return !now.Before(last.Add(time.Duration(cfg.Monitoring.CheckIntervalMinutes) * time.Minute))
}

func (m *Monitor) runAlerts(ctx context.Context) {
	res, err := m.alerts.Run(ctx)
	now := time.Now().UTC()
	m.mu.Lock()
	m.lastAlertRun = &now
	m.lastAlertRes = res
	m.lastAlertErr = ""
	if err != nil {
		m.lastAlertErr = err.Error()
	}
	m.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		m.log.Error("alerts", "err", err)
	}
	if res.Created > 0 || res.Sent > 0 || res.Failed > 0 {
		m.log.Info("alerts processed", "created", res.Created, "sent", res.Sent, "failed", res.Failed,
			"cancelled", res.Cancelled)
	}
}

func (m *Monitor) housekeeping(ctx context.Context) {
	cfg, err := m.settings.Get(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	idle := now.Add(-time.Duration(max(cfg.Security.SessionIdleMinutes, 0)) * time.Minute)
	if cfg.Security.SessionIdleMinutes == 0 {
		idle = time.Unix(0, 0)
	}
	if n, err := m.store.DeleteExpiredSessions(ctx, now, idle); err != nil {
		m.log.Error("cleanup sessions", "err", err)
	} else if n > 0 {
		m.log.Debug("expired sessions removed", "count", n)
	}
	if _, err := m.store.DeleteStaleUserTokens(ctx, now.Add(-7*24*time.Hour)); err != nil {
		m.log.Error("cleanup tokens", "err", err)
	}
	keep := now.AddDate(0, 0, -cfg.Monitoring.HistoryRetentionDays)
	if _, err := m.store.PruneNotifications(ctx, keep, now.Add(-alerts.ExpiredLookback)); err != nil {
		m.log.Error("prune notifications", "err", err)
	}
	if _, err := m.store.PruneAudit(ctx, keep); err != nil {
		m.log.Error("prune audit log", "err", err)
	}
	if _, err := m.store.PruneMailLog(ctx, keep); err != nil {
		m.log.Error("prune mail log", "err", err)
	}
}
