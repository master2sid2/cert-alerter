package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
)

const userCols = `id, email, name, role, status, password_hash, failed_logins, locked_until,
	password_changed_at, last_login_at, invited_by, created_at, updated_at`

func scanUser(sc scanner) (*User, error) {
	var (
		u                                       User
		locked, pwChanged, lastLogin, invitedBy sql.NullInt64
		created, updated                        int64
	)
	if err := sc.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.PasswordHash, &u.FailedLogins,
		&locked, &pwChanged, &lastLogin, &invitedBy, &created, &updated); err != nil {
		return nil, err
	}
	u.LockedUntil = fromNullUnix(locked)
	u.PasswordChangedAt = fromNullUnix(pwChanged)
	u.LastLoginAt = fromNullUnix(lastLogin)
	u.InvitedBy = fromNullInt(invitedBy)
	u.CreatedAt = fromUnix(created)
	u.UpdatedAt = fromUnix(updated)
	return &u, nil
}

// NormalizeEmail lower-cases and trims an e-mail address.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// CountUsers returns the number of user accounts (any status).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CountActiveAdmins returns the number of active admin accounts.
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = ? AND status = ?",
		RoleAdmin, UserActive).Scan(&n)
	return n, err
}

// CreateUser inserts a user and fills in ID and timestamps.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	now := s.now()
	u.Email = NormalizeEmail(u.Email)
	u.CreatedAt, u.UpdatedAt = now, now
	res, err := s.DB.ExecContext(ctx, `INSERT INTO users
		(email, name, role, status, password_hash, failed_logins, password_changed_at, invited_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		u.Email, u.Name, u.Role, u.Status, u.PasswordHash, unixOrNil(u.PasswordChangedAt),
		int64OrNil(u.InvitedBy), now.Unix(), now.Unix())
	if err != nil {
		return mapWriteErr(err)
	}
	u.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	if u.PasswordHash != "" {
		_, err = s.DB.ExecContext(ctx,
			"INSERT INTO password_history (user_id, password_hash, created_at) VALUES (?, ?, ?)",
			u.ID, u.PasswordHash, now.Unix())
	}
	return err
}

// GetUser loads a user by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	return u, notFound(err)
}

// GetUserByEmail loads a user by e-mail (case-insensitive).
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE email = ?",
		NormalizeEmail(email)))
	return u, notFound(err)
}

// ListUsers returns all users ordered by e-mail.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUserProfile changes a user's display name.
func (s *Store) UpdateUserProfile(ctx context.Context, id int64, name string) error {
	return expectOne(s.DB.ExecContext(ctx, "UPDATE users SET name = ?, updated_at = ? WHERE id = ?",
		name, s.now().Unix(), id))
}

// UpdateUserAccess changes a user's name, role and status.
func (s *Store) UpdateUserAccess(ctx context.Context, id int64, name, role, status string) error {
	return expectOne(s.DB.ExecContext(ctx,
		"UPDATE users SET name = ?, role = ?, status = ?, updated_at = ? WHERE id = ?",
		name, role, status, s.now().Unix(), id))
}

// SetUserPassword stores a new password hash, records it in the password
// history (keeping at most keepHistory entries) and optionally activates an
// invited account. Failed login counters are reset.
func (s *Store) SetUserPassword(ctx context.Context, id int64, hash string, activate bool, keepHistory int) error {
	now := s.now().Unix()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		q := "UPDATE users SET password_hash = ?, password_changed_at = ?, failed_logins = 0, locked_until = NULL, updated_at = ?"
		args := []any{hash, now, now}
		if activate {
			q += ", status = ?"
			args = append(args, UserActive)
		}
		q += " WHERE id = ?"
		args = append(args, id)
		if err := expectOne(tx.ExecContext(ctx, q, args...)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO password_history (user_id, password_hash, created_at) VALUES (?, ?, ?)",
			id, hash, now); err != nil {
			return err
		}
		keepHistory = max(keepHistory, 1)
		ids, err := queryInt64s(ctx, tx,
			"SELECT id FROM password_history WHERE user_id = ? ORDER BY id DESC", id)
		if err != nil {
			return err
		}
		if len(ids) > keepHistory {
			old := ids[keepHistory:]
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM password_history WHERE id IN ("+db.Placeholders(len(old))+")", int64Args(old)...); err != nil {
				return err
			}
		}
		return nil
	})
}

// PasswordHistory returns the most recent password hashes of a user.
func (s *Store) PasswordHistory(ctx context.Context, userID int64, limit int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT password_hash FROM password_history WHERE user_id = ? ORDER BY id DESC LIMIT ?", userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// IncrementFailedLogins bumps the failed login counter and returns the new value.
func (s *Store) IncrementFailedLogins(ctx context.Context, id int64) (int, error) {
	if _, err := s.DB.ExecContext(ctx, "UPDATE users SET failed_logins = failed_logins + 1 WHERE id = ?", id); err != nil {
		return 0, err
	}
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT failed_logins FROM users WHERE id = ?", id).Scan(&n)
	return n, notFound(err)
}

// LockUser locks an account until the given time and resets the counter.
func (s *Store) LockUser(ctx context.Context, id int64, until time.Time) error {
	return expectOne(s.DB.ExecContext(ctx,
		"UPDATE users SET locked_until = ?, failed_logins = 0, updated_at = ? WHERE id = ?",
		until.Unix(), s.now().Unix(), id))
}

// UnlockUser clears a lock and the failed login counter.
func (s *Store) UnlockUser(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx,
		"UPDATE users SET locked_until = NULL, failed_logins = 0, updated_at = ? WHERE id = ?",
		s.now().Unix(), id))
}

// RecordLoginSuccess resets failure counters and stores the login time.
func (s *Store) RecordLoginSuccess(ctx context.Context, id int64) error {
	now := s.now().Unix()
	return expectOne(s.DB.ExecContext(ctx,
		"UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = ? WHERE id = ?", now, id))
}

// DeleteUser removes a user; sessions, tokens and history cascade.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id))
}
