package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
)

// GetSetting returns the raw value of a setting.
func (s *Store) GetSetting(ctx context.Context, name string) (string, bool, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE name = ?", name).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

// AllSettings returns all settings as a map.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT name, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting inserts or replaces a setting (portable upsert).
func (s *Store) SetSetting(ctx context.Context, name, value string) error {
	now := s.now().Unix()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE settings SET value = ?, updated_at = ? WHERE name = ?", value, now, name)
		if err != nil {
			return err
		}
		// MySQL connections use CLIENT_FOUND_ROWS, so matched rows count.
		if n, err := res.RowsAffected(); err != nil || n > 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO settings (name, value, updated_at) VALUES (?, ?, ?)", name, value, now)
		return err
	})
}

// AddAudit appends an audit log entry.
func (s *Store) AddAudit(ctx context.Context, e *AuditEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.now()
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO audit_log
		(user_id, user_email, action, entity_type, entity_id, details, ip, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		int64OrNil(e.UserID), truncate(e.UserEmail, 255), truncate(e.Action, 64), truncate(e.EntityType, 32),
		int64OrNil(e.EntityID), truncate(e.Details, 2000), truncate(e.IP, 64), e.CreatedAt.Unix())
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

// AuditFilter narrows ListAudit.
type AuditFilter struct {
	Query  string
	Limit  int
	Offset int
}

// ListAudit returns audit entries newest first plus the total count.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, int, error) {
	cond := ""
	var args []any
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		cond = ` WHERE LOWER(user_email) LIKE ? ESCAPE '!' OR LOWER(action) LIKE ? ESCAPE '!' OR LOWER(details) LIKE ? ESCAPE '!'`
		args = append(args, like, like, like)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log"+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, user_email, action, entity_type, entity_id, details, ip, created_at
		FROM audit_log`+cond+" ORDER BY id DESC LIMIT ? OFFSET ?", append(args, limit, max(f.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var (
			e             AuditEntry
			uid, entityID sql.NullInt64
			created       int64
		)
		if err := rows.Scan(&e.ID, &uid, &e.UserEmail, &e.Action, &e.EntityType, &entityID, &e.Details, &e.IP, &created); err != nil {
			return nil, 0, err
		}
		e.UserID, e.EntityID = fromNullInt(uid), fromNullInt(entityID)
		e.CreatedAt = fromUnix(created)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// PruneAudit deletes audit entries older than t.
func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM audit_log WHERE created_at < ?", before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TryLock acquires or renews a named lease for owner. It returns false
// when another owner holds an unexpired lease. Used to elect the single
// instance that runs scheduled jobs when several share one database.
func (s *Store) TryLock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	now := s.now()
	exp := now.Add(ttl).Unix()
	res, err := s.DB.ExecContext(ctx,
		"UPDATE app_locks SET owner = ?, expires_at = ? WHERE name = ? AND (owner = ? OR expires_at < ?)",
		owner, exp, name, owner, now.Unix())
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n > 0 {
		return true, nil
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO app_locks (name, owner, expires_at) VALUES (?, ?, ?)", name, owner, exp)
	if err == nil {
		return true, nil
	}
	if db.IsUniqueViolation(err) {
		return false, nil
	}
	return false, err
}

// Unlock releases a lease held by owner.
func (s *Store) Unlock(ctx context.Context, name, owner string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM app_locks WHERE name = ? AND owner = ?", name, owner)
	return err
}
