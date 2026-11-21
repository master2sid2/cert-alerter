package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/master2sid2/cert-alerter/internal/db"
)

const recipientCols = "id, email, name, created_at, updated_at"

func scanRecipient(sc scanner) (*Recipient, error) {
	var (
		r                Recipient
		created, updated int64
	)
	if err := sc.Scan(&r.ID, &r.Email, &r.Name, &created, &updated); err != nil {
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt = fromUnix(created), fromUnix(updated)
	r.GroupIDs = []int64{}
	return &r, nil
}

// ListRecipients returns all recipients with their group ids.
func (s *Store) ListRecipients(ctx context.Context) ([]Recipient, error) {
	groups, err := s.RecipientGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+recipientCols+" FROM recipients ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recipient{}
	for rows.Next() {
		r, err := scanRecipient(rows)
		if err != nil {
			return nil, err
		}
		if g := groups[r.ID]; g != nil {
			r.GroupIDs = g
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRecipient loads a recipient with group ids.
func (s *Store) GetRecipient(ctx context.Context, id int64) (*Recipient, error) {
	r, err := scanRecipient(s.DB.QueryRowContext(ctx, "SELECT "+recipientCols+" FROM recipients WHERE id = ?", id))
	if err != nil {
		return nil, notFound(err)
	}
	ids, err := queryInt64s(ctx, s.DB, "SELECT group_id FROM group_recipients WHERE recipient_id = ? ORDER BY group_id", id)
	if err != nil {
		return nil, err
	}
	if ids != nil {
		r.GroupIDs = ids
	}
	return r, nil
}

// GetRecipientByEmail loads a recipient by address.
func (s *Store) GetRecipientByEmail(ctx context.Context, email string) (*Recipient, error) {
	r, err := scanRecipient(s.DB.QueryRowContext(ctx, "SELECT "+recipientCols+" FROM recipients WHERE email = ?",
		NormalizeEmail(email)))
	return r, notFound(err)
}

// CreateRecipient inserts a recipient.
func (s *Store) CreateRecipient(ctx context.Context, r *Recipient) error {
	now := s.now()
	r.Email = NormalizeEmail(r.Email)
	res, err := s.DB.ExecContext(ctx, "INSERT INTO recipients (email, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
		r.Email, r.Name, now.Unix(), now.Unix())
	if err != nil {
		return mapWriteErr(err)
	}
	r.CreatedAt, r.UpdatedAt = now, now
	if r.GroupIDs == nil {
		r.GroupIDs = []int64{}
	}
	r.ID, err = res.LastInsertId()
	return err
}

// EnsureRecipient returns the recipient with the given e-mail, creating it
// when missing. The bool reports whether it was created.
func (s *Store) EnsureRecipient(ctx context.Context, email, name string) (*Recipient, bool, error) {
	r, err := s.GetRecipientByEmail(ctx, email)
	if err == nil {
		return r, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	r = &Recipient{Email: email, Name: name}
	if err := s.CreateRecipient(ctx, r); err != nil {
		if errors.Is(err, ErrConflict) { // created concurrently
			r, err = s.GetRecipientByEmail(ctx, email)
			return r, false, err
		}
		return nil, false, err
	}
	return r, true, nil
}

// UpdateRecipient changes e-mail and name.
func (s *Store) UpdateRecipient(ctx context.Context, r *Recipient) error {
	r.Email = NormalizeEmail(r.Email)
	return expectOne(s.DB.ExecContext(ctx, "UPDATE recipients SET email = ?, name = ?, updated_at = ? WHERE id = ?",
		r.Email, r.Name, s.now().Unix(), r.ID))
}

// SetRecipientGroups replaces the group memberships of a recipient.
func (s *Store) SetRecipientGroups(ctx context.Context, id int64, groupIDs []int64) error {
	groupIDs = uniqueIDs(groupIDs)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if len(groupIDs) > 0 {
			var known int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_groups WHERE id IN ("+
				db.Placeholders(len(groupIDs))+")", int64Args(groupIDs)...).Scan(&known); err != nil {
				return err
			}
			if known != len(groupIDs) {
				return ErrNotFound
			}
		}
		current, err := queryInt64s(ctx, tx, "SELECT group_id FROM group_recipients WHERE recipient_id = ?", id)
		if err != nil {
			return err
		}
		for _, g := range current {
			if !slices.Contains(groupIDs, g) {
				if _, err := tx.ExecContext(ctx, "DELETE FROM group_recipients WHERE group_id = ? AND recipient_id = ?", g, id); err != nil {
					return err
				}
			}
		}
		now := s.now().Unix()
		for _, g := range groupIDs {
			if !slices.Contains(current, g) {
				if _, err := tx.ExecContext(ctx, s.DB.InsertIgnore("group_recipients", "group_id", "recipient_id", "created_at"),
					g, id, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// DeleteRecipient removes a recipient from all groups and deletes it.
func (s *Store) DeleteRecipient(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, "DELETE FROM recipients WHERE id = ?", id))
}
