package db

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations
var migrationsFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(d Dialect) ([]migration, error) {
	dir := path.Join("migrations", string(d))
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must look like 0001_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(migrationsFS, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	slices.SortFunc(out, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	return out, nil
}

// LatestVersion returns the newest embedded schema version for the dialect.
func LatestVersion(d Dialect) int {
	ms, err := loadMigrations(d)
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// Migrate applies all pending embedded migrations. On MySQL a named lock
// serialises concurrent application instances starting at the same time.
func (d *DB) Migrate(ctx context.Context) (applied int, err error) {
	ms, err := loadMigrations(d.Dialect)
	if err != nil {
		return 0, fmt.Errorf("load migrations: %w", err)
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if d.Dialect == MySQL {
		var got sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('cert_alerter_migrate', 60)").Scan(&got); err != nil {
			return 0, fmt.Errorf("acquire migration lock: %w", err)
		}
		if !got.Valid || got.Int64 != 1 {
			return 0, fmt.Errorf("could not acquire migration lock")
		}
		defer func() {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK('cert_alerter_migrate')")
		}()
	}

	create := `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL)`
	if d.Dialect == MySQL {
		create = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT NOT NULL PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		applied_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
	}
	if _, err := conn.ExecContext(ctx, create); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	done := map[int]bool{}
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, err
		}
		done[v] = true
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	for _, m := range ms {
		if done[m.version] {
			continue
		}
		if err := d.applyMigration(ctx, conn, m); err != nil {
			return applied, fmt.Errorf("migration %s: %w", m.name, err)
		}
		applied++
	}
	return applied, nil
}

func (d *DB) applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	stmts := SplitStatements(m.sql)
	record := "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)"
	now := time.Now().Unix()

	if d.Dialect == SQLite {
		// SQLite DDL is transactional: all or nothing.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		for _, s := range stmts {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("%w\nstatement: %s", err, s)
			}
		}
		if _, err := tx.ExecContext(ctx, record, m.version, m.name, now); err != nil {
			return err
		}
		return tx.Commit()
	}

	// MySQL commits DDL implicitly; statements run one by one.
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%w\nstatement: %s", err, s)
		}
	}
	_, err := conn.ExecContext(ctx, record, m.version, m.name, now)
	return err
}

// SplitStatements splits a SQL script on semicolons that are outside of
// quotes and strips "--" line comments.
func SplitStatements(script string) []string {
	var (
		out     []string
		cur     strings.Builder
		inQuote rune
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	runes := []rune(script)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inQuote != 0 {
			cur.WriteRune(r)
			if r == inQuote {
				inQuote = 0
			}
			continue
		}
		switch {
		case r == '\'' || r == '"' || r == '`':
			inQuote = r
			cur.WriteRune(r)
		case r == '-' && i+1 < len(runes) && runes[i+1] == '-':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			cur.WriteRune('\n')
		case r == ';':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// Tables lists application tables in an order that satisfies foreign keys
// (parents first). Used by the database copy tool.
var Tables = []string{
	"users",
	"password_history",
	"sessions",
	"user_tokens",
	"alert_groups",
	"recipients",
	"group_recipients",
	"certificates",
	"group_certificates",
	"channels",
	"group_channels",
	"notifications",
	"mail_log",
	"settings",
	"audit_log",
	"app_locks",
}
