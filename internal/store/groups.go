package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/master2sid2/cert-alerter/internal/db"
)

const groupSelect = `SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
	(SELECT COUNT(*) FROM group_certificates gc WHERE gc.group_id = g.id),
	(SELECT COUNT(*) FROM group_recipients gr WHERE gr.group_id = g.id)
	FROM alert_groups g`

func scanGroup(sc scanner) (*Group, error) {
	var (
		g                Group
		created, updated int64
	)
	if err := sc.Scan(&g.ID, &g.Name, &g.Description, &created, &updated, &g.CertificateCount, &g.RecipientCount); err != nil {
		return nil, err
	}
	g.CreatedAt, g.UpdatedAt = fromUnix(created), fromUnix(updated)
	return &g, nil
}

// ListGroups returns all groups with membership counts, ordered by name.
func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.DB.QueryContext(ctx, groupSelect+" ORDER BY g.name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Group) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// GetGroup loads a group with counts.
func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
	g, err := scanGroup(s.DB.QueryRowContext(ctx, groupSelect+" WHERE g.id = ?", id))
	return g, notFound(err)
}

// GroupNameTaken reports whether another group already uses name
// (case-insensitive, Unicode aware, identical on all databases).
func (s *Store) GroupNameTaken(ctx context.Context, name string, exceptID int64) (bool, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name FROM alert_groups")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return false, err
		}
		if id != exceptID && strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(name)) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// CreateGroup inserts a group.
func (s *Store) CreateGroup(ctx context.Context, g *Group) error {
	if taken, err := s.GroupNameTaken(ctx, g.Name, 0); err != nil {
		return err
	} else if taken {
		return ErrConflict
	}
	now := s.now()
	res, err := s.DB.ExecContext(ctx,
		"INSERT INTO alert_groups (name, description, created_at, updated_at) VALUES (?, ?, ?, ?)",
		g.Name, g.Description, now.Unix(), now.Unix())
	if err != nil {
		return mapWriteErr(err)
	}
	g.CreatedAt, g.UpdatedAt = now, now
	g.ID, err = res.LastInsertId()
	return err
}

// UpdateGroup changes name and description.
func (s *Store) UpdateGroup(ctx context.Context, g *Group) error {
	if taken, err := s.GroupNameTaken(ctx, g.Name, g.ID); err != nil {
		return err
	} else if taken {
		return ErrConflict
	}
	return expectOne(s.DB.ExecContext(ctx,
		"UPDATE alert_groups SET name = ?, description = ?, updated_at = ? WHERE id = ?",
		g.Name, g.Description, s.now().Unix(), g.ID))
}

// DeleteGroup removes a group and its memberships. Notification history
// is kept (group_id becomes NULL, the name snapshot stays).
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, "DELETE FROM alert_groups WHERE id = ?", id))
}

// AddCertificatesToGroup links certificates to a group and returns the
// number of new links. Unknown certificate ids yield ErrNotFound.
func (s *Store) AddCertificatesToGroup(ctx context.Context, groupID int64, certIDs []int64, by *int64) (int, error) {
	return s.addLinks(ctx, "group_certificates", "certificate_id", "certificates", groupID, certIDs, by)
}

// AddRecipientsToGroup links recipients to a group and returns the number
// of new links. Unknown recipient ids yield ErrNotFound.
func (s *Store) AddRecipientsToGroup(ctx context.Context, groupID int64, recipientIDs []int64) (int, error) {
	return s.addLinks(ctx, "group_recipients", "recipient_id", "recipients", groupID, recipientIDs, nil)
}

func (s *Store) addLinks(ctx context.Context, table, col, refTable string, groupID int64, ids []int64, by *int64) (int, error) {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	added := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_groups WHERE id = ?", groupID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		args := int64Args(ids)
		var known int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+refTable+" WHERE id IN ("+db.Placeholders(len(ids))+")",
			args...).Scan(&known); err != nil {
			return err
		}
		if known != len(ids) {
			return ErrNotFound
		}
		present, err := queryInt64s(ctx, tx, "SELECT "+col+" FROM "+table+" WHERE group_id = ? AND "+col+
			" IN ("+db.Placeholders(len(ids))+")", append([]any{groupID}, args...)...)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		for _, id := range ids {
			if slices.Contains(present, id) {
				continue
			}
			if table == "group_certificates" {
				_, err = tx.ExecContext(ctx, s.DB.InsertIgnore(table, "group_id", col, "created_by", "created_at"),
					groupID, id, int64OrNil(by), now)
			} else {
				_, err = tx.ExecContext(ctx, s.DB.InsertIgnore(table, "group_id", col, "created_at"), groupID, id, now)
			}
			if err != nil {
				return err
			}
			added++
		}
		return nil
	})
	return added, err
}

// RemoveCertificateFromGroup unlinks a certificate from a group.
func (s *Store) RemoveCertificateFromGroup(ctx context.Context, groupID, certID int64) error {
	return expectOne(s.DB.ExecContext(ctx,
		"DELETE FROM group_certificates WHERE group_id = ? AND certificate_id = ?", groupID, certID))
}

// RemoveRecipientFromGroup unlinks a recipient from a group.
func (s *Store) RemoveRecipientFromGroup(ctx context.Context, groupID, recipientID int64) error {
	return expectOne(s.DB.ExecContext(ctx,
		"DELETE FROM group_recipients WHERE group_id = ? AND recipient_id = ?", groupID, recipientID))
}

// GroupCertificateIDs returns ids of certificates in a group.
func (s *Store) GroupCertificateIDs(ctx context.Context, groupID int64) ([]int64, error) {
	return queryInt64s(ctx, s.DB, "SELECT certificate_id FROM group_certificates WHERE group_id = ?", groupID)
}

// GroupRecipients returns the recipients of a group ordered by e-mail.
func (s *Store) GroupRecipients(ctx context.Context, groupID int64) ([]Recipient, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.email, r.name, r.created_at, r.updated_at
		FROM recipients r JOIN group_recipients gr ON gr.recipient_id = r.id
		WHERE gr.group_id = ? ORDER BY r.email`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		r, err := scanRecipient(rows)
		if err != nil {
			return nil, err
		}
		r.GroupIDs = []int64{groupID}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CertificateGroupIDs maps certificate ids to the ids of their groups.
func (s *Store) CertificateGroupIDs(ctx context.Context) (map[int64][]int64, error) {
	return collectPairs(ctx, s.DB, "SELECT certificate_id, group_id FROM group_certificates ORDER BY group_id")
}

// RecipientGroupIDs maps recipient ids to the ids of their groups.
func (s *Store) RecipientGroupIDs(ctx context.Context) (map[int64][]int64, error) {
	return collectPairs(ctx, s.DB, "SELECT recipient_id, group_id FROM group_recipients ORDER BY group_id")
}

func uniqueIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	out = slices.Compact(out)
	return slices.DeleteFunc(out, func(id int64) bool { return id <= 0 })
}
