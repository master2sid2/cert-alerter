package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/master2sid2/cert-alerter/internal/store"
)

type bucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	groups, byID, err := s.groupViews(r, cfg)
	if err != nil {
		return err
	}
	recipients, err := s.Store.ListRecipients(ctx)
	if err != nil {
		return err
	}
	now := time.Now()

	certs := make([]certView, 0, len(byID))
	for _, c := range byID {
		certs = append(certs, c)
	}
	slices.SortFunc(certs, compareExpiry)

	totals := map[string]int{
		"certificates": len(certs), store.StatusOK: 0, store.StatusWarning: 0, store.StatusCritical: 0,
		store.StatusExpired: 0, store.StatusError: 0, store.StatusPending: 0,
		"check_failed": 0, "untrusted": 0, "disabled": 0, "ungrouped": 0,
	}
	// Expiry distribution of enabled certificates with known expiry.
	bucketKeys := []string{"expired", "0-7", "8-14", "15-30", "31-60", "61-90", "90+"}
	bucketCounts := map[string]int{}
	// Weekly forecast for the next 12 weeks.
	forecast := make([]int, 12)

	var expiring, problems []certView
	for _, c := range certs {
		if !c.Enabled {
			totals["disabled"]++
			continue
		}
		totals[c.Status]++
		if c.CheckFailed {
			totals["check_failed"]++
		}
		if c.NotAfter != nil && !c.Trusted {
			totals["untrusted"]++
		}
		if len(c.GroupIDs) == 0 {
			totals["ungrouped"]++
		}
		if c.DaysLeft != nil {
			d := *c.DaysLeft
			switch {
			case c.Status == store.StatusExpired:
				bucketCounts["expired"]++
			case d <= 7:
				bucketCounts["0-7"]++
			case d <= 14:
				bucketCounts["8-14"]++
			case d <= 30:
				bucketCounts["15-30"]++
			case d <= 60:
				bucketCounts["31-60"]++
			case d <= 90:
				bucketCounts["61-90"]++
			default:
				bucketCounts["90+"]++
			}
			if d >= 0 && d < 7*len(forecast) {
				forecast[d/7]++
			}
			if len(expiring) < 8 && (c.Status == store.StatusExpired || c.Status == store.StatusCritical ||
				c.Status == store.StatusWarning) {
				expiring = append(expiring, c)
			}
		}
		if (c.CheckFailed || (c.NotAfter != nil && !c.Trusted)) && len(problems) < 8 {
			problems = append(problems, c)
		}
	}
	buckets := make([]bucket, len(bucketKeys))
	for i, k := range bucketKeys {
		buckets[i] = bucket{Key: k, Count: bucketCounts[k]}
	}

	notifCounts, err := s.Store.NotificationCounts(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		return err
	}
	recent, _, err := s.Store.ListNotifications(ctx, store.NotificationFilter{Limit: 8})
	if err != nil {
		return err
	}
	pending, err := s.Store.CountPendingNotifications(ctx)
	if err != nil {
		return err
	}
	mail30, err := s.Store.MailLogCounts(ctx, now.AddDate(0, 0, -30), mailKindsFor(r))
	if err != nil {
		return err
	}

	// Groups ordered by urgency.
	slices.SortStableFunc(groups, func(a, b groupView) int {
		ra, rb := 99, 99
		if a.Health.Worst != "" {
			ra = statusRank[a.Health.Worst]
		}
		if b.Health.Worst != "" {
			rb = statusRank[b.Health.Worst]
		}
		return ra - rb
	})
	groupsCount := len(groups)
	if len(groups) > 8 {
		groups = groups[:8]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totals":                totals,
		"groups_count":          groupsCount,
		"recipients_count":      len(recipients),
		"buckets":               buckets,
		"forecast":              forecast,
		"expiring":              nonNilCerts(expiring),
		"problems":              nonNilCerts(problems),
		"groups":                groups,
		"notifications_30d":     notifCounts,
		"notifications_pending": pending,
		"mail_30d":              mail30,
		"recent_notifications":  recent,
		"scheduler":             s.Monitor.Status(ctx),
		"smtp_configured":       cfg.SMTP.Configured(),
		"thresholds":            cfg.Monitoring.Thresholds,
		"generated_at":          now.UTC(),
	})
	return nil
}

func nonNilCerts(v []certView) []certView {
	if v == nil {
		return []certView{}
	}
	return v
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := store.NotificationFilter{
		Status:  q.Get("status"),
		Channel: q.Get("channel"),
		Query:   q.Get("q"),
		Limit:   queryInt(r, "limit", 50),
		Offset:  queryInt(r, "offset", 0),
	}
	if v, err := strconv.ParseInt(q.Get("group_id"), 10, 64); err == nil {
		f.GroupID = v
	}
	if v, err := strconv.ParseInt(q.Get("certificate_id"), 10, 64); err == nil {
		f.CertificateID = v
	}
	items, total, err := s.Store.ListNotifications(r.Context(), f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
	return nil
}

func (s *Server) handleRetryNotification(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.RetryNotification(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errConflict("not_failed", "only failed notifications can be retried")
		}
		return err
	}
	s.audit(r, "notification.retried", "notification", id, nil)
	s.Monitor.KickAlerts()
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) error {
	items, total, err := s.Store.ListAudit(r.Context(), store.AuditFilter{
		Query:  r.URL.Query().Get("q"),
		Limit:  queryInt(r, "limit", 50),
		Offset: queryInt(r, "offset", 0),
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
	return nil
}
