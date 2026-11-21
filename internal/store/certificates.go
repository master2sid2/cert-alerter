package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const certCols = `id, url, host, port, name, notes, enabled, created_by, created_at, updated_at,
	last_checked_at, last_success_at, last_error, subject_cn, subject_org, issuer_cn, issuer_org, sans,
	serial_number, fingerprint_sha256, signature_algorithm, key_algorithm, key_bits, not_before, not_after,
	trusted, verify_error, tls_version, resolved_ip, chain`

func scanCertificate(sc scanner) (*Certificate, error) {
	var (
		c                                             Certificate
		createdBy, lastChecked, lastSuccess, nbf, naf sql.NullInt64
		created, updated                              int64
		sans, chain                                   sql.NullString
	)
	if err := sc.Scan(&c.ID, &c.URL, &c.Host, &c.Port, &c.Name, &c.Notes, &c.Enabled, &createdBy, &created, &updated,
		&lastChecked, &lastSuccess, &c.LastError, &c.SubjectCN, &c.SubjectOrg, &c.IssuerCN, &c.IssuerOrg, &sans,
		&c.SerialNumber, &c.FingerprintSHA256, &c.SignatureAlgorithm, &c.KeyAlgorithm, &c.KeyBits, &nbf, &naf,
		&c.Trusted, &c.VerifyError, &c.TLSVersion, &c.ResolvedIP, &chain); err != nil {
		return nil, err
	}
	c.CreatedBy = fromNullInt(createdBy)
	c.CreatedAt, c.UpdatedAt = fromUnix(created), fromUnix(updated)
	c.LastCheckedAt = fromNullUnix(lastChecked)
	c.LastSuccessAt = fromNullUnix(lastSuccess)
	c.NotBefore = fromNullUnix(nbf)
	c.NotAfter = fromNullUnix(naf)
	c.SANs = []string{}
	if sans.Valid && sans.String != "" {
		_ = json.Unmarshal([]byte(sans.String), &c.SANs)
	}
	c.Chain = []ChainCert{}
	if chain.Valid && chain.String != "" {
		_ = json.Unmarshal([]byte(chain.String), &c.Chain)
	}
	c.GroupIDs = []int64{}
	return &c, nil
}

// ListCertificates returns all certificates with their group ids.
func (s *Store) ListCertificates(ctx context.Context) ([]Certificate, error) {
	groups, err := s.CertificateGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+certCols+" FROM certificates ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Certificate{}
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		if g := groups[c.ID]; g != nil {
			c.GroupIDs = g
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetCertificate loads one certificate with its group ids.
func (s *Store) GetCertificate(ctx context.Context, id int64) (*Certificate, error) {
	c, err := scanCertificate(s.DB.QueryRowContext(ctx, "SELECT "+certCols+" FROM certificates WHERE id = ?", id))
	if err != nil {
		return nil, notFound(err)
	}
	ids, err := queryInt64s(ctx, s.DB, "SELECT group_id FROM group_certificates WHERE certificate_id = ? ORDER BY group_id", id)
	if err != nil {
		return nil, err
	}
	if ids != nil {
		c.GroupIDs = ids
	}
	return c, nil
}

// FindCertificateByEndpoint loads a certificate by host and port.
func (s *Store) FindCertificateByEndpoint(ctx context.Context, host string, port int) (*Certificate, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM certificates WHERE host = ? AND port = ?", host, port).Scan(&id)
	if err != nil {
		return nil, notFound(err)
	}
	return s.GetCertificate(ctx, id)
}

// CreateCertificate inserts a monitored endpoint.
func (s *Store) CreateCertificate(ctx context.Context, c *Certificate) error {
	now := s.now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO certificates
		(url, host, port, name, notes, enabled, created_by, created_at, updated_at, sans, chain)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.URL, c.Host, c.Port, c.Name, c.Notes, c.Enabled, int64OrNil(c.CreatedBy), now.Unix(), now.Unix(), "[]", "[]")
	if err != nil {
		return mapWriteErr(err)
	}
	c.CreatedAt, c.UpdatedAt = now, now
	c.SANs, c.Chain = []string{}, []ChainCert{}
	if c.GroupIDs == nil {
		c.GroupIDs = []int64{}
	}
	c.ID, err = res.LastInsertId()
	return err
}

// UpdateCertificate changes the editable fields. When the endpoint (host or
// port) changes, data of the previously seen certificate is discarded.
func (s *Store) UpdateCertificate(ctx context.Context, c *Certificate) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var host string
		var port int
		if err := tx.QueryRowContext(ctx, "SELECT host, port FROM certificates WHERE id = ?", c.ID).Scan(&host, &port); err != nil {
			return notFound(err)
		}
		now := s.now().Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE certificates SET url = ?, host = ?, port = ?, name = ?, notes = ?,
			enabled = ?, updated_at = ? WHERE id = ?`,
			c.URL, c.Host, c.Port, c.Name, c.Notes, c.Enabled, now, c.ID); err != nil {
			return mapWriteErr(err)
		}
		if host != c.Host || port != c.Port {
			_, err := tx.ExecContext(ctx, `UPDATE certificates SET last_checked_at = NULL, last_success_at = NULL,
				last_error = '', subject_cn = '', subject_org = '', issuer_cn = '', issuer_org = '', sans = '[]',
				serial_number = '', fingerprint_sha256 = '', signature_algorithm = '', key_algorithm = '', key_bits = 0,
				not_before = NULL, not_after = NULL, trusted = ?, verify_error = '', tls_version = '', resolved_ip = '',
				chain = '[]' WHERE id = ?`, false, c.ID)
			return err
		}
		return nil
	})
}

// DeleteCertificate removes a certificate and its group links.
func (s *Store) DeleteCertificate(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, "DELETE FROM certificates WHERE id = ?", id))
}

// SaveCheckOutcome persists the result of a TLS check. A failed check only
// updates the error and the check timestamp; certificate data of the last
// successful check is preserved so expiry alerts keep working while an
// endpoint is temporarily unreachable.
func (s *Store) SaveCheckOutcome(ctx context.Context, id int64, o CheckOutcome) error {
	checked := o.CheckedAt.Unix()
	if o.Error != "" {
		return expectOne(s.DB.ExecContext(ctx,
			"UPDATE certificates SET last_checked_at = ?, last_error = ? WHERE id = ?",
			checked, truncate(o.Error, 1000), id))
	}
	sans, err := json.Marshal(nonNil(o.SANs))
	if err != nil {
		return err
	}
	chain, err := json.Marshal(nonNilChain(o.Chain))
	if err != nil {
		return err
	}
	return expectOne(s.DB.ExecContext(ctx, `UPDATE certificates SET last_checked_at = ?, last_success_at = ?,
		last_error = '', subject_cn = ?, subject_org = ?, issuer_cn = ?, issuer_org = ?, sans = ?, serial_number = ?,
		fingerprint_sha256 = ?, signature_algorithm = ?, key_algorithm = ?, key_bits = ?, not_before = ?, not_after = ?,
		trusted = ?, verify_error = ?, tls_version = ?, resolved_ip = ?, chain = ? WHERE id = ?`,
		checked, checked, truncate(o.SubjectCN, 255), truncate(o.SubjectOrg, 255), truncate(o.IssuerCN, 255),
		truncate(o.IssuerOrg, 255), string(sans), truncate(o.SerialNumber, 128), o.FingerprintSHA256,
		truncate(o.SignatureAlgorithm, 64), truncate(o.KeyAlgorithm, 32), o.KeyBits, o.NotBefore.Unix(),
		o.NotAfter.Unix(), o.Trusted, truncate(o.VerifyError, 1000), truncate(o.TLSVersion, 16),
		truncate(o.ResolvedIP, 64), string(chain), id))
}

// ListCheckTargets returns enabled endpoints. When onlyUnchecked is true,
// only endpoints that were never checked are returned.
func (s *Store) ListCheckTargets(ctx context.Context, onlyUnchecked bool) ([]CheckTarget, error) {
	q := "SELECT id, host, port FROM certificates WHERE enabled = ?"
	if onlyUnchecked {
		q += " AND last_checked_at IS NULL"
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY id", true)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckTarget
	for rows.Next() {
		var t CheckTarget
		if err := rows.Scan(&t.ID, &t.Host, &t.Port); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastCheckTime returns the most recent check timestamp of any certificate.
func (s *Store) LastCheckTime(ctx context.Context) (*time.Time, error) {
	var v sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, "SELECT MAX(last_checked_at) FROM certificates").Scan(&v); err != nil {
		return nil, err
	}
	return fromNullUnix(v), nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilChain(v []ChainCert) []ChainCert {
	if v == nil {
		return []ChainCert{}
	}
	return v
}
