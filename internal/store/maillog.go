package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
)

// Mail kinds.
const (
	MailAlert     = "alert"
	MailGroupTest = "group_test"
	MailTest      = "test"
	MailInvite    = "invite"
	MailReset     = "reset"
)

// Mail statuses. "sent" means the SMTP server accepted the message.
const (
	MailSent   = "sent"
	MailFailed = "failed"
)

// Delivery channels of the journal besides the messenger types.
const ChannelEmail = "email"

// MailLogEntry records one attempt to deliver one message: an e-mail or a
// messenger message (Channel is the provider type, Recipient the channel
// name and SMTPServer the host that was contacted).
type MailLogEntry struct {
	ID           int64     `json:"id"`
	Channel      string    `json:"channel"`
	Kind         string    `json:"kind"`
	Recipient    string    `json:"recipient"`
	Subject      string    `json:"subject"`
	Status       string    `json:"status"`
	Error        string    `json:"error"`
	GroupID      *int64    `json:"group_id"`
	GroupName    string    `json:"group_name"`
	Certificates []string  `json:"certificates"`
	CertCount    int       `json:"cert_count"`
	SMTPServer   string    `json:"smtp_server"`
	DurationMS   int       `json:"duration_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

// AddMailLog appends an entry to the mail journal.
func (s *Store) AddMailLog(ctx context.Context, e *MailLogEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.now()
	}
	if e.Channel == "" {
		e.Channel = ChannelEmail
	}
	certs := strings.Join(e.Certificates, "\n")
	res, err := s.DB.ExecContext(ctx, `INSERT INTO mail_log
		(channel, kind, recipient, subject, status, error, group_id, group_name, certificates, cert_count, smtp_server, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		truncate(e.Channel, 32), truncate(e.Kind, 16), truncate(e.Recipient, 255), truncate(e.Subject, 500), e.Status, truncate(e.Error, 1000),
		int64OrNil(e.GroupID), truncate(e.GroupName, 255), truncate(certs, 4000), e.CertCount,
		truncate(e.SMTPServer, 300), e.DurationMS, e.CreatedAt.Unix())
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

// MailLogFilter narrows ListMailLog.
type MailLogFilter struct {
	Channel string   // "" = all
	Kinds   []string // empty = all
	Status  string
	GroupID int64
	Query   string // recipient, subject, group or certificate substring
	From    time.Time
	To      time.Time
	Limit   int
	Offset  int
}

func (f MailLogFilter) where() (string, []any) {
	var (
		conds []string
		args  []any
	)
	if len(f.Kinds) > 0 {
		conds = append(conds, "kind IN ("+db.Placeholders(len(f.Kinds))+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Channel != "" {
		conds = append(conds, "channel = ?")
		args = append(args, f.Channel)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.GroupID > 0 {
		conds = append(conds, "group_id = ?")
		args = append(args, f.GroupID)
	}
	if !f.From.IsZero() {
		conds = append(conds, "created_at >= ?")
		args = append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		conds = append(conds, "created_at < ?")
		args = append(args, f.To.Unix())
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		conds = append(conds, `(LOWER(recipient) LIKE ? ESCAPE '!' OR LOWER(subject) LIKE ? ESCAPE '!'
			OR LOWER(group_name) LIKE ? ESCAPE '!' OR LOWER(certificates) LIKE ? ESCAPE '!')`)
		args = append(args, like, like, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListMailLog returns journal entries, newest first, and the total count.
func (s *Store) ListMailLog(ctx context.Context, f MailLogFilter) ([]MailLogEntry, int, error) {
	cond, args := f.where()
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM mail_log"+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 10000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, channel, kind, recipient, subject, status, error, group_id, group_name,
		certificates, cert_count, smtp_server, duration_ms, created_at FROM mail_log`+cond+
		" ORDER BY id DESC LIMIT ? OFFSET ?", append(args, limit, max(f.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []MailLogEntry{}
	for rows.Next() {
		var (
			e       MailLogEntry
			gid     sql.NullInt64
			certs   string
			created int64
		)
		if err := rows.Scan(&e.ID, &e.Channel, &e.Kind, &e.Recipient, &e.Subject, &e.Status, &e.Error, &gid, &e.GroupName,
			&certs, &e.CertCount, &e.SMTPServer, &e.DurationMS, &created); err != nil {
			return nil, 0, err
		}
		e.GroupID = fromNullInt(gid)
		e.Certificates = []string{}
		if certs != "" {
			e.Certificates = strings.Split(certs, "\n")
		}
		e.CreatedAt = fromUnix(created)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// MailLogCounts returns counts per status since t (optionally per kinds).
func (s *Store) MailLogCounts(ctx context.Context, since time.Time, kinds []string) (map[string]int, error) {
	cond, args := MailLogFilter{From: since, Kinds: kinds}.where()
	rows, err := s.DB.QueryContext(ctx, "SELECT status, COUNT(*) FROM mail_log"+cond+" GROUP BY status", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{MailSent: 0, MailFailed: 0}
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

// PruneMailLog deletes journal entries created before t.
func (s *Store) PruneMailLog(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM mail_log WHERE created_at < ?", before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
