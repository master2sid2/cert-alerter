package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// Channel is a messenger destination (Telegram chat, Slack channel,
// webhook) that receives the alerts of its groups.
type Channel struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Config holds the provider fields as stored: secret values are
	// encrypted. It is never serialised as is.
	Config    map[string]string `json:"-"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	GroupIDs  []int64           `json:"group_ids"`
}

const channelCols = "id, type, name, enabled, config, created_at, updated_at"

func scanChannel(sc scanner) (*Channel, error) {
	var (
		c                Channel
		raw              string
		created, updated int64
	)
	if err := sc.Scan(&c.ID, &c.Type, &c.Name, &c.Enabled, &raw, &created, &updated); err != nil {
		return nil, err
	}
	c.Config = map[string]string{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.Config); err != nil {
			return nil, err
		}
	}
	c.CreatedAt, c.UpdatedAt = fromUnix(created), fromUnix(updated)
	c.GroupIDs = []int64{}
	return &c, nil
}

// ListChannels returns all channels with their group ids, ordered by name.
func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	groups, err := collectPairs(ctx, s.DB, "SELECT channel_id, group_id FROM group_channels ORDER BY group_id")
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+channelCols+" FROM channels")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		if g := groups[c.ID]; g != nil {
			c.GroupIDs = g
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Channel) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// GetChannel loads a channel with its group ids.
func (s *Store) GetChannel(ctx context.Context, id int64) (*Channel, error) {
	c, err := scanChannel(s.DB.QueryRowContext(ctx, "SELECT "+channelCols+" FROM channels WHERE id = ?", id))
	if err != nil {
		return nil, notFound(err)
	}
	c.GroupIDs, err = queryInt64s(ctx, s.DB, "SELECT group_id FROM group_channels WHERE channel_id = ? ORDER BY group_id", id)
	if c.GroupIDs == nil {
		c.GroupIDs = []int64{}
	}
	return c, err
}

// ChannelNameTaken reports whether another channel uses name
// (case-insensitive, identical on all databases).
func (s *Store) ChannelNameTaken(ctx context.Context, name string, exceptID int64) (bool, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name FROM channels")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			n  string
		)
		if err := rows.Scan(&id, &n); err != nil {
			return false, err
		}
		if id != exceptID && strings.EqualFold(n, name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func encodeConfig(cfg map[string]string) (string, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	b, err := json.Marshal(cfg)
	return string(b), err
}

// CreateChannel inserts a channel.
func (s *Store) CreateChannel(ctx context.Context, c *Channel) error {
	raw, err := encodeConfig(c.Config)
	if err != nil {
		return err
	}
	now := s.now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO channels (type, name, enabled, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		c.Type, c.Name, c.Enabled, raw, now.Unix(), now.Unix())
	if err != nil {
		return mapWriteErr(err)
	}
	c.CreatedAt, c.UpdatedAt = now, now
	if c.GroupIDs == nil {
		c.GroupIDs = []int64{}
	}
	c.ID, err = res.LastInsertId()
	return err
}

// UpdateChannel stores name, enabled flag and configuration (the type
// cannot change).
func (s *Store) UpdateChannel(ctx context.Context, c *Channel) error {
	raw, err := encodeConfig(c.Config)
	if err != nil {
		return err
	}
	return expectOne(s.DB.ExecContext(ctx, "UPDATE channels SET name = ?, enabled = ?, config = ?, updated_at = ? WHERE id = ?",
		c.Name, c.Enabled, raw, s.now().Unix(), c.ID))
}

// DeleteChannel deletes a channel; its notification history is kept.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, "DELETE FROM channels WHERE id = ?", id))
}

// SetChannelGroups replaces the groups whose alerts a channel receives.
func (s *Store) SetChannelGroups(ctx context.Context, id int64, groupIDs []int64) error {
	return s.setGroups(ctx, "group_channels", "channel_id", id, groupIDs)
}

// AddChannelsToGroup links channels to a group and returns the number of
// new links. Unknown channel ids yield ErrNotFound.
func (s *Store) AddChannelsToGroup(ctx context.Context, groupID int64, channelIDs []int64) (int, error) {
	return s.addLinks(ctx, "group_channels", "channel_id", "channels", groupID, channelIDs, nil)
}

// RemoveChannelFromGroup unlinks a channel from a group.
func (s *Store) RemoveChannelFromGroup(ctx context.Context, groupID, channelID int64) error {
	return expectOne(s.DB.ExecContext(ctx,
		"DELETE FROM group_channels WHERE group_id = ? AND channel_id = ?", groupID, channelID))
}

// GroupChannels returns the channels of a group ordered by name.
func (s *Store) GroupChannels(ctx context.Context, groupID int64) ([]Channel, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.type, c.name, c.enabled, c.config, c.created_at, c.updated_at
		FROM channels c JOIN group_channels gc ON gc.channel_id = c.id WHERE gc.group_id = ?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		c.GroupIDs = []int64{groupID}
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Channel) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, rows.Err()
}

// ChannelAlertCandidate is a (certificate, group, channel) triple whose
// certificate is close to expiry.
type ChannelAlertCandidate struct {
	CertificateID  int64
	CertificateURL string
	NotAfter       time.Time
	GroupID        int64
	GroupName      string
	ChannelID      int64
	ChannelName    string
	ChannelType    string
}

// ListChannelAlertCandidates returns every (certificate, group, channel)
// triple of enabled certificates and enabled channels whose expiry lies in
// [from, to).
func (s *Store) ListChannelAlertCandidates(ctx context.Context, from, to time.Time) ([]ChannelAlertCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.url, c.not_after, g.id, g.name, ch.id, ch.name, ch.type
		FROM certificates c
		JOIN group_certificates gc ON gc.certificate_id = c.id
		JOIN alert_groups g ON g.id = gc.group_id
		JOIN group_channels gch ON gch.group_id = g.id
		JOIN channels ch ON ch.id = gch.channel_id
		WHERE c.enabled = ? AND ch.enabled = ? AND c.not_after IS NOT NULL AND c.not_after >= ? AND c.not_after < ?
		ORDER BY c.id, g.id, ch.id`, true, true, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelAlertCandidate
	for rows.Next() {
		var (
			a        ChannelAlertCandidate
			notAfter int64
		)
		if err := rows.Scan(&a.CertificateID, &a.CertificateURL, &notAfter, &a.GroupID, &a.GroupName,
			&a.ChannelID, &a.ChannelName, &a.ChannelType); err != nil {
			return nil, err
		}
		a.NotAfter = fromUnix(notAfter)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ChannelNotificationKey identifies one alert stage of one channel.
type ChannelNotificationKey struct {
	GroupID, CertificateID, ChannelID, NotAfter int64
	Threshold                                   int
}

// ChannelNotificationKeys returns the keys of channel notifications for
// certificates whose expiry lies in [from, to).
func (s *Store) ChannelNotificationKeys(ctx context.Context, from, to time.Time) (map[ChannelNotificationKey]struct{}, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT group_id, certificate_id, channel_id, not_after, threshold_days
		FROM notifications WHERE not_after >= ? AND not_after < ?
		AND group_id IS NOT NULL AND certificate_id IS NOT NULL AND channel_id IS NOT NULL`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ChannelNotificationKey]struct{}{}
	for rows.Next() {
		var k ChannelNotificationKey
		if err := rows.Scan(&k.GroupID, &k.CertificateID, &k.ChannelID, &k.NotAfter, &k.Threshold); err != nil {
			return nil, err
		}
		out[k] = struct{}{}
	}
	return out, rows.Err()
}
