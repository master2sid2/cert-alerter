package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// CopyStats reports rows copied per table.
type CopyStats struct {
	Table string
	Rows  int
}

// CopyData copies all application data from src to dst (for example from
// SQLite to MySQL). Both databases must be migrated to the same schema
// version; dst must be empty unless force is set, in which case existing
// rows in dst are deleted first. Primary keys are preserved.
func CopyData(ctx context.Context, src, dst *DB, force bool, progress func(CopyStats)) error {
	srcVer, err := schemaVersion(ctx, src)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	dstVer, err := schemaVersion(ctx, dst)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if srcVer != dstVer {
		return fmt.Errorf("schema versions differ (source %d, target %d); run migrations first", srcVer, dstVer)
	}

	conn, err := dst.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Rows are inserted parent-first, but self references and the deletion
	// below are easier with constraint checks disabled on this connection.
	switch dst.Dialect {
	case MySQL:
		_, err = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	case SQLite:
		_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	}
	if err != nil {
		return err
	}
	defer func() {
		switch dst.Dialect {
		case MySQL:
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SET FOREIGN_KEY_CHECKS = 1")
		case SQLite:
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
		}
	}()

	for _, table := range Tables {
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		if n > 0 && !force {
			return fmt.Errorf("target table %s is not empty (%d rows); use -force to overwrite", table, n)
		}
	}
	if force {
		for i := len(Tables) - 1; i >= 0; i-- {
			if _, err := conn.ExecContext(ctx, "DELETE FROM "+Tables[i]); err != nil {
				return fmt.Errorf("clear %s: %w", Tables[i], err)
			}
		}
	}
	for _, table := range Tables {
		n, err := copyTable(ctx, src, conn, table)
		if err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
		if progress != nil {
			progress(CopyStats{Table: table, Rows: n})
		}
	}
	return nil
}

func schemaVersion(ctx context.Context, d *DB) (int, error) {
	var v sql.NullInt64
	if err := d.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

func copyTable(ctx context.Context, src *DB, dst *sql.Conn, table string) (int, error) {
	rows, err := src.QueryContext(ctx, "SELECT * FROM "+table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return 0, err
	}
	intCol := make([]bool, len(cols))
	for i, t := range types {
		intCol[i] = strings.Contains(strings.ToUpper(t.DatabaseTypeName()), "INT")
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), Placeholders(len(cols)))

	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	stmt, err := tx.PrepareContext(ctx, insert)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return count, err
		}
		args := make([]any, len(vals))
		for i, v := range vals {
			// MySQL returns text and (in the text protocol) numbers as
			// []byte; SQLite would store those as BLOBs, so convert.
			if b, ok := v.([]byte); ok {
				if intCol[i] {
					n, err := strconv.ParseInt(string(b), 10, 64)
					if err != nil {
						return count, fmt.Errorf("column %s: %w", cols[i], err)
					}
					args[i] = n
				} else {
					args[i] = string(b)
				}
				continue
			}
			args[i] = v
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, tx.Commit()
}
