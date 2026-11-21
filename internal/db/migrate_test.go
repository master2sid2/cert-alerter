package db_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/master2sid2/cert-alerter/internal/db"
)

func TestSplitStatements(t *testing.T) {
	got := db.SplitStatements("-- comment; ignored\nCREATE TABLE a (x TEXT DEFAULT 'a;b');\n\nINSERT INTO a VALUES ('--x'); -- tail\n")
	want := []string{"CREATE TABLE a (x TEXT DEFAULT 'a;b')", "INSERT INTO a VALUES ('--x')"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	n, err := d.Migrate(ctx)
	if err != nil || n != db.LatestVersion(db.SQLite) {
		t.Fatalf("first migrate: %d %v", n, err)
	}
	if n, err := d.Migrate(ctx); err != nil || n != 0 {
		t.Fatalf("second migrate: %d %v", n, err)
	}
	if db.LatestVersion(db.SQLite) != db.LatestVersion(db.MySQL) {
		t.Fatal("SQLite and MySQL migrations must stay in lock-step")
	}
}

func TestSQLiteDSN(t *testing.T) {
	dir := t.TempDir()
	dsn, err := db.SQLiteDSN(filepath.Join(dir, "sub", "a.db") + "?_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_foreign_keys=1", "_journal_mode=WAL", "_txlock=immediate"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q lacks %s", dsn, want)
		}
	}
	if strings.Contains(dsn, "_busy_timeout=") {
		t.Errorf("explicit busy_timeout pragma must win: %q", dsn)
	}
	if _, err := db.NormalizeDriver("postgres"); err == nil {
		t.Error("unsupported driver accepted")
	}
}
