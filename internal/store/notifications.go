package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
)

// ListAlertCandidates returns every (certificate, group, recipient) triple
// of enabled certificates whose expiry lies in [from, to).
func (s *Store) ListAlertCandidates(ctx context.Context, from, to time.Time) ([]AlertCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.url, c.not_after, g.id, g.name, r.id, r.email
		FROM certificates c
		JOIN group_certificates gc ON gc.certificate_id = c.id
		JOIN alert_groups g ON g.id = gc.group_id
		JOIN group_recipients gr ON gr.group_id = g.id
		JOIN recipients r ON r.id = gr.recipient_id
		WHERE c.enabled = ? AND c.not_after IS NOT NULL AND c.not_after >= ? AND c.not_after < ?
		ORDER BY c.id, g.id, r.id`, true, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertCandidate
	for rows.Next() {
		var (
			a        AlertCandidate
			notAfter int64
		)
		if err := rows.Scan(&a.CertificateID, &a.CertificateURL, &notAfter, &a.GroupID, &a.GroupName,
			&a.RecipientID, &a.RecipientEmail); err != nil {
			return nil, err
		}
		a.NotAfter = fromUnix(notAfter)
		out = append(out, a)
	}
	return out, rows.Err()
}

// NotificationKey identifies one alert stage of one recipient.
type NotificationKey struct {
	GroupID, CertificateID, RecipientID, NotAfter int64
	Threshold                                     int
}

// NotificationKeys returns the keys of notifications for certificates whose
// expiry lies in [from, to).
func (s *Store) NotificationKeys(ctx context.Context, from, to time.Time) (map[NotificationKey]struct{}, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT group_id, certificate_id, recipient_id, not_after, threshold_days
		FROM notifications WHERE not_after >= ? AND not_after < ?
		AND group_id IS NOT NULL AND certificate_id IS NOT NULL AND recipient_id IS NOT NULL`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[NotificationKey]struct{}{}
	for rows.Next() {
		var k NotificationKey
		if err := rows.Scan(&k.GroupID, &k.CertificateID, &k.RecipientID, &k.NotAfter, &k.Threshold); err != nil {
			return nil, err
		}
		out[k] = struct{}{}
	}
	return out, rows.Err()
}

// InsertNotification creates a pending notification unless one with the
// same (group, certificate, recipient, not_after, threshold) exists.
// It reports whether a row was created.
func (s *Store) InsertNotification(ctx context.Context, n *Notification) (bool, error) {
	now := s.now()
	if n.NextAttemptAt.IsZero() {
		n.NextAttemptAt = now
	}
	n.Status = NotificationPending
	n.CreatedAt = now
	res, err := s.DB.ExecContext(ctx, `INSERT INTO notifications
		(group_id, certificate_id, recipient_id, group_name, certificate_url, recipient_email, not_after,
		 threshold_days, days_left, status, attempts, last_error, next_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)`,
		int64OrNil(n.GroupID), int64OrNil(n.CertificateID), int64OrNil(n.RecipientID),
		truncate(n.GroupName, 255), truncate(n.CertificateURL, 2048), truncate(n.RecipientEmail, 255),
		n.NotAfter.Unix(), n.ThresholdDays, n.DaysLeft, n.Status, n.NextAttemptAt.Unix(), now.Unix())
	if err != nil {
		if db.IsUniqueViolation(err) {
			return false, nil
		}
		return false, err
	}
	n.ID, err = res.LastInsertId()
	return true, err
}

// ListDueNotifications returns pending notifications whose next attempt is
// due, joined with the current certificate/group/recipient state.
func (s *Store) ListDueNotifications(ctx context.Context, now time.Time, limit int) ([]DueNotification, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id, n.group_id, n.certificate_id, n.recipient_id, n.group_name,
			n.certificate_url, n.recipient_email, n.not_after, n.threshold_days, n.days_left, n.status, n.attempts,
			n.last_error, n.next_attempt_at, n.created_at,
			c.id, c.url, c.name, c.subject_cn, c.issuer_cn, c.not_after, c.enabled,
			g.id, g.name, g.description, r.id, r.email, r.name,
			gc.group_id, gr.group_id
		FROM notifications n
		LEFT JOIN certificates c ON c.id = n.certificate_id
		LEFT JOIN alert_groups g ON g.id = n.group_id
		LEFT JOIN recipients r ON r.id = n.recipient_id
		LEFT JOIN group_certificates gc ON gc.group_id = n.group_id AND gc.certificate_id = n.certificate_id
		LEFT JOIN group_recipients gr ON gr.group_id = n.group_id AND gr.recipient_id = n.recipient_id
		WHERE n.status = ? AND n.next_attempt_at <= ?
		ORDER BY n.id LIMIT ?`, NotificationPending, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueNotification
	for rows.Next() {
		var (
			d                             DueNotification
			gid, cid, rid                 sql.NullInt64
			notAfter, next, created       int64
			cID, gID, rID, gcLink, grLink sql.NullInt64
			cURL, cName, cCN, cIssuer     sql.NullString
			gName, gDesc, rEmail, rName   sql.NullString
			cNotAfter                     sql.NullInt64
			cEnabled                      sql.NullBool
		)
		if err := rows.Scan(&d.ID, &gid, &cid, &rid, &d.GroupName, &d.CertificateURL, &d.RecipientEmail, &notAfter,
			&d.ThresholdDays, &d.DaysLeft, &d.Status, &d.Attempts, &d.LastError, &next, &created,
			&cID, &cURL, &cName, &cCN, &cIssuer, &cNotAfter, &cEnabled,
			&gID, &gName, &gDesc, &rID, &rEmail, &rName, &gcLink, &grLink); err != nil {
			return nil, err
		}
		d.GroupID, d.CertificateID, d.RecipientID = fromNullInt(gid), fromNullInt(cid), fromNullInt(rid)
		d.NotAfter, d.NextAttemptAt, d.CreatedAt = fromUnix(notAfter), fromUnix(next), fromUnix(created)
		d.CurrentNotAfter = fromNullUnix(cNotAfter)
		d.Valid = cID.Valid && gID.Valid && rID.Valid && gcLink.Valid && grLink.Valid &&
			cEnabled.Valid && cEnabled.Bool && cNotAfter.Valid && cNotAfter.Int64 == notAfter
		if cURL.Valid {
			d.CertificateURL = cURL.String
		}
		d.CertificateName, d.SubjectCN, d.IssuerCN = cName.String, cCN.String, cIssuer.String
		if gName.Valid {
			d.GroupName = gName.String
		}
		d.GroupDesc = gDesc.String
		if rEmail.Valid {
			d.RecipientEmail = rEmail.String
		}
		d.RecipientName = rName.String
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkNotificationsSent flags notifications as delivered.
func (s *Store) MarkNotificationsSent(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]any{NotificationSent, at.Unix()}, int64Args(ids)...)
	_, err := s.DB.ExecContext(ctx, "UPDATE notifications SET status = ?, sent_at = ?, attempts = attempts + 1, last_error = '' WHERE id IN ("+
		db.Placeholders(len(ids))+")", args...)
	return err
}

// MarkNotificationsFailed records a failed delivery attempt. When final is
// true the notifications are not retried any more.
func (s *Store) MarkNotificationsFailed(ctx context.Context, ids []int64, errMsg string, next time.Time, final bool) error {
	if len(ids) == 0 {
		return nil
	}
	status := NotificationPending
	if final {
		status = NotificationFailed
	}
	args := append([]any{status, truncate(errMsg, 1000), next.Unix()}, int64Args(ids)...)
	_, err := s.DB.ExecContext(ctx, "UPDATE notifications SET status = ?, last_error = ?, next_attempt_at = ?, attempts = attempts + 1 WHERE id IN ("+
		db.Placeholders(len(ids))+")", args...)
	return err
}

// CancelNotifications marks pending notifications as cancelled (stale or
// superseded by a more urgent threshold).
func (s *Store) CancelNotifications(ctx context.Context, ids []int64, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]any{NotificationCancelled, truncate(reason, 1000)}, int64Args(ids)...)
	args = append(args, NotificationPending)
	_, err := s.DB.ExecContext(ctx, "UPDATE notifications SET status = ?, last_error = ? WHERE id IN ("+
		db.Placeholders(len(ids))+") AND status = ?", args...)
	return err
}

// NotificationFilter narrows ListNotifications.
type NotificationFilter struct {
	Status        string
	GroupID       int64
	CertificateID int64
	Query         string // substring of recipient e-mail, URL or group name
	Limit         int
	Offset        int
}

// ListNotifications returns notification history, newest first, plus the
// total number of matching rows.
func (s *Store) ListNotifications(ctx context.Context, f NotificationFilter) ([]Notification, int, error) {
	var (
		where []string
		args  []any
	)
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.GroupID > 0 {
		where = append(where, "group_id = ?")
		args = append(args, f.GroupID)
	}
	if f.CertificateID > 0 {
		where = append(where, "certificate_id = ?")
		args = append(args, f.CertificateID)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		where = append(where, `(LOWER(recipient_email) LIKE ? ESCAPE '!' OR LOWER(certificate_url) LIKE ? ESCAPE '!' OR LOWER(group_name) LIKE ? ESCAPE '!')`)
		args = append(args, like, like, like)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications"+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, group_id, certificate_id, recipient_id, group_name, certificate_url,
		recipient_email, not_after, threshold_days, days_left, status, attempts, last_error, next_attempt_at,
		created_at, sent_at FROM notifications`+cond+" ORDER BY id DESC LIMIT ? OFFSET ?",
		append(args, limit, max(f.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var (
			n                       Notification
			gid, cid, rid, sent     sql.NullInt64
			notAfter, next, created int64
		)
		if err := rows.Scan(&n.ID, &gid, &cid, &rid, &n.GroupName, &n.CertificateURL, &n.RecipientEmail, &notAfter,
			&n.ThresholdDays, &n.DaysLeft, &n.Status, &n.Attempts, &n.LastError, &next, &created, &sent); err != nil {
			return nil, 0, err
		}
		n.GroupID, n.CertificateID, n.RecipientID = fromNullInt(gid), fromNullInt(cid), fromNullInt(rid)
		n.NotAfter, n.NextAttemptAt, n.CreatedAt = fromUnix(notAfter), fromUnix(next), fromUnix(created)
		n.SentAt = fromNullUnix(sent)
		out = append(out, n)
	}
	return out, total, rows.Err()
}

// NotificationCounts returns notification counts per status created since t.
func (s *Store) NotificationCounts(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT status, COUNT(*) FROM notifications WHERE created_at >= ? GROUP BY status",
		since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// PruneNotifications deletes finished notifications created before t whose
// certificate expiry is older than keepNotAfter (newer ones are still
// needed to avoid re-sending alerts).
func (s *Store) PruneNotifications(ctx context.Context, before, keepNotAfter time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM notifications WHERE created_at < ? AND not_after < ? AND status <> ?",
		before.Unix(), keepNotAfter.Unix(), NotificationPending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// escapeLike escapes LIKE wildcards using '!' as escape character.
func escapeLike(s string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(s)
}

// RetryNotification puts a failed notification back into the queue.
func (s *Store) RetryNotification(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx,
		"UPDATE notifications SET status = ?, attempts = 0, next_attempt_at = ? WHERE id = ? AND status = ?",
		NotificationPending, s.now().Unix(), id, NotificationFailed))
}

// CountPendingNotifications returns the number of queued notifications.
func (s *Store) CountPendingNotifications(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE status = ?", NotificationPending).Scan(&n)
	return n, err
}
