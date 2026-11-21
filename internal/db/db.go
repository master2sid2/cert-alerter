// Package db opens the application database (SQLite or MySQL/MariaDB),
// applies embedded schema migrations and hides the few SQL dialect
// differences the rest of the application has to care about.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Dialect identifies the SQL flavour of the connected database.
type Dialect string

const (
	SQLite Dialect = "sqlite"
	MySQL  Dialect = "mysql"
)

// DB wraps *sql.DB and remembers which dialect it speaks.
type DB struct {
	*sql.DB
	Dialect Dialect
}

// ErrUnsupportedDriver is returned by Open for unknown driver names.
var ErrUnsupportedDriver = errors.New("unsupported database driver (use sqlite or mysql)")

// NormalizeDriver maps user supplied driver aliases to a Dialect.
func NormalizeDriver(driver string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "", "sqlite", "sqlite3":
		return SQLite, nil
	case "mysql", "mariadb":
		return MySQL, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnsupportedDriver, driver)
}

// Open connects to the database and verifies the connection.
func Open(ctx context.Context, driver, dsn string) (*DB, error) {
	dialect, err := NormalizeDriver(driver)
	if err != nil {
		return nil, err
	}
	var sqlDB *sql.DB
	switch dialect {
	case SQLite:
		sqlDB, err = openSQLite(dsn)
	case MySQL:
		sqlDB, err = openMySQL(dsn)
	}
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("connect to %s database: %w", dialect, err)
	}
	return &DB{DB: sqlDB, Dialect: dialect}, nil
}

func openSQLite(dsn string) (*sql.DB, error) {
	full, err := SQLiteDSN(dsn)
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("sqlite", full)
	if err != nil {
		return nil, err
	}
	// WAL mode allows concurrent readers next to a single writer; writers
	// wait on busy_timeout. A small pool is plenty for this workload.
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)
	return sqlDB, nil
}

// SQLiteDSN turns a plain path (or file: URI) into a modernc.org/sqlite DSN
// with the pragmas the application relies on (foreign keys, WAL, busy
// timeout, immediate transactions). Parameters given explicitly win.
func SQLiteDSN(dsn string) (string, error) {
	if strings.TrimSpace(dsn) == "" {
		dsn = filepath.Join("data", "cert-alerter.db")
	}
	path, query, _ := strings.Cut(strings.TrimPrefix(dsn, "file:"), "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("invalid sqlite dsn: %w", err)
	}
	inMemory := path == ":memory:" || q.Get("mode") == "memory"
	if !inMemory {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return "", fmt.Errorf("create database directory: %w", err)
			}
		}
	}
	hasPragma := func(name string) bool {
		for _, p := range q["_pragma"] {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), name) {
				return true
			}
		}
		return false
	}
	setDefault := func(key, alias, pragma, value string) {
		if q.Has(key) || (alias != "" && q.Has(alias)) || hasPragma(pragma) {
			return
		}
		q.Set(key, value)
	}
	setDefault("_busy_timeout", "_timeout", "busy_timeout", "10000")
	setDefault("_foreign_keys", "_fk", "foreign_keys", "1")
	if !inMemory {
		setDefault("_journal_mode", "_journal", "journal_mode", "WAL")
	}
	setDefault("_synchronous", "_sync", "synchronous", "NORMAL")
	if !q.Has("_txlock") {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode(), nil
}

func openMySQL(dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("mysql requires a DSN, e.g. user:password@tcp(127.0.0.1:3306)/cert_alerter")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid mysql dsn: %w", err)
	}
	// Timestamps are stored as unix seconds, but keep the session sane anyway.
	cfg.Loc = time.UTC
	cfg.ParseTime = true
	// Report matched (not only changed) rows for UPDATE so "not found" checks
	// behave identically on SQLite and MySQL.
	cfg.ClientFoundRows = true
	if cfg.Collation == "" {
		cfg.Collation = "utf8mb4_unicode_ci"
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid mysql dsn: %w", err)
	}
	sqlDB := sql.OpenDB(connector)
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	return sqlDB, nil
}

// InsertIgnore returns an INSERT statement that silently skips rows violating
// a unique/primary key, without hiding any other kind of error.
//
// cols are the inserted columns; the statement uses one placeholder per column.
func (d *DB) InsertIgnore(table string, cols ...string) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	base := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), placeholders)
	if d.Dialect == MySQL {
		// "INSERT IGNORE" would also swallow FK violations; a no-op update does not.
		return base + fmt.Sprintf(" ON DUPLICATE KEY UPDATE %s = %s", cols[0], cols[0])
	}
	return base + " ON CONFLICT DO NOTHING"
}

// IsUniqueViolation reports whether err is a unique/primary key violation.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		code := se.Code()
		return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return me.Number == 1062
	}
	return false
}

// IsForeignKeyViolation reports whether err is a foreign key violation.
func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		return se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	}
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return me.Number == 1451 || me.Number == 1452
	}
	return false
}

// Placeholders returns "?, ?, ?" with n placeholders (n >= 1).
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
