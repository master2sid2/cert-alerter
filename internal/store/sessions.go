package store

import (
	"context"
	"database/sql"
	"time"
)

// CreateSession stores a new login session.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, user_id, created_at, last_seen_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.CreatedAt.Unix(), sess.LastSeenAt.Unix(), sess.ExpiresAt.Unix(),
		truncate(sess.IP, 64), truncate(sess.UserAgent, 255))
	return mapWriteErr(err)
}

// GetSession loads a session by id (hash of the cookie token).
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	var (
		sess                      Session
		created, seen, expiresRaw int64
	)
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, created_at, last_seen_at, expires_at, ip, user_agent
		FROM sessions WHERE id = ?`, id).Scan(&sess.ID, &sess.UserID, &created, &seen, &expiresRaw, &sess.IP, &sess.UserAgent)
	if err != nil {
		return nil, notFound(err)
	}
	sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = fromUnix(created), fromUnix(seen), fromUnix(expiresRaw)
	return &sess, nil
}

// TouchSession updates the last activity timestamp.
func (s *Store) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE id = ?", at.Unix(), id)
	return err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	return err
}

// DeleteUserSessions removes all sessions of a user except exceptID.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, exceptID string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, exceptID)
	return err
}

// DeleteExpiredSessions removes sessions that expired before now or were
// idle since before idleBefore.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now, idleBefore time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?",
		now.Unix(), idleBefore.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountUserSessions returns active session counts per user.
func (s *Store) CountUserSessions(ctx context.Context, now time.Time) (map[int64]int, error) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT user_id, COUNT(*) FROM sessions WHERE expires_at >= ? GROUP BY user_id", now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// CreateUserToken stores a new token, replacing unused tokens of the same
// purpose for the user.
func (s *Store) CreateUserToken(ctx context.Context, t *UserToken, hash string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_tokens WHERE user_id = ? AND purpose = ?",
			t.UserID, t.Purpose); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO user_tokens
			(token_hash, user_id, purpose, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			hash, t.UserID, t.Purpose, int64OrNil(t.CreatedBy), t.CreatedAt.Unix(), t.ExpiresAt.Unix())
		if err != nil {
			return mapWriteErr(err)
		}
		t.ID, err = res.LastInsertId()
		return err
	})
}

// GetUserTokenByHash loads a token by the hash of its secret.
func (s *Store) GetUserTokenByHash(ctx context.Context, hash string) (*UserToken, error) {
	var (
		t                 UserToken
		createdBy, usedAt sql.NullInt64
		created, expires  int64
	)
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, purpose, created_by, created_at, expires_at, used_at
		FROM user_tokens WHERE token_hash = ?`, hash).
		Scan(&t.ID, &t.UserID, &t.Purpose, &createdBy, &created, &expires, &usedAt)
	if err != nil {
		return nil, notFound(err)
	}
	t.CreatedBy = fromNullInt(createdBy)
	t.CreatedAt, t.ExpiresAt = fromUnix(created), fromUnix(expires)
	t.UsedAt = fromNullUnix(usedAt)
	return &t, nil
}

// ConsumeUserToken marks a token as used. It returns false when the token
// was already used (protects against double submission races).
func (s *Store) ConsumeUserToken(ctx context.Context, id int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE user_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL",
		s.now().Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteUserTokens removes tokens of a user (purpose "" = all purposes).
func (s *Store) DeleteUserTokens(ctx context.Context, userID int64, purpose string) error {
	if purpose == "" {
		_, err := s.DB.ExecContext(ctx, "DELETE FROM user_tokens WHERE user_id = ?", userID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, "DELETE FROM user_tokens WHERE user_id = ? AND purpose = ?", userID, purpose)
	return err
}

// DeleteStaleUserTokens removes used tokens and tokens that expired before t.
func (s *Store) DeleteStaleUserTokens(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM user_tokens WHERE used_at IS NOT NULL OR expires_at < ?", t.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InviteExpiries maps invited user ids to the expiry of their open invite.
func (s *Store) InviteExpiries(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT user_id, expires_at FROM user_tokens WHERE purpose = ? AND used_at IS NULL", TokenInvite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id, exp int64
		if err := rows.Scan(&id, &exp); err != nil {
			return nil, err
		}
		out[id] = fromUnix(exp)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
