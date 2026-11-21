// Package store implements data access on top of database/sql. All SQL is
// written to run unchanged on SQLite and MySQL; the few dialect differences
// are delegated to package db.
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/master2sid2/cert-alerter/internal/db"
)

var (
	// ErrNotFound is returned when a requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a unique constraint would be violated.
	ErrConflict = errors.New("already exists")
)

// Store provides typed access to the application database.
type Store struct {
	DB *db.DB
	// Now is the clock used for timestamps; replaceable in tests.
	Now func() time.Time
}

// New creates a Store.
func New(d *db.DB) *Store {
	return &Store{DB: d, Now: time.Now}
}

func (s *Store) now() time.Time { return s.Now().UTC().Truncate(time.Second) }

type scanner interface {
	Scan(dest ...any) error
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn inside a transaction, committing on success.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapWriteErr(err error) error {
	if db.IsUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// expectOne converts "0 rows affected" into ErrNotFound.
func expectOne(res sql.Result, err error) error {
	if err != nil {
		return mapWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func unixOrNil(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

func fromNullUnix(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	return new(time.Unix(v.Int64, 0).UTC())
}

func fromNullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return new(v.Int64)
}

func int64OrNil(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// int64Args converts ids to query arguments.
func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// collectIDs runs a query that returns (key, value) int64 pairs and groups
// values by key.
func collectPairs(ctx context.Context, q execer, query string, args ...any) (map[int64][]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var k, v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = append(out[k], v)
	}
	return out, rows.Err()
}

func queryInt64s(ctx context.Context, q execer, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
