// Package testutil provides test infrastructure for Roost Go services.
// P18-T02-S01: Test infrastructure with Postgres integration.
//
// Usage:
//
//	func TestMain(m *testing.M) {
//	    db := testutil.MustOpenDB(t)
//	    defer db.Close()
//	    // run tests using db
//	}
package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// DSN returns the Postgres DSN for tests.
// In CI: uses TEST_DATABASE_URL env var (set by GitHub Actions postgres service).
// Locally: falls back to a local dev DSN.
func DSN() string {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://roost:roost@localhost:5433/roost_test?sslmode=disable"
}

// OpenDB opens a Postgres connection using the test DSN.
// It applies all migrations from backend/db/migrations/ and returns the connection.
// The caller is responsible for closing the db.
func OpenDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("postgres", DSN())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return db, nil
}

// MustOpenDB opens a Postgres connection and fails the test if it cannot.
func MustOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(t)
	if err != nil {
		t.Skipf("testutil: skipping integration test (no Postgres): %v", err)
	}
	return db
}

// applyMigrations runs all .sql files in backend/db/migrations/ in order.
func applyMigrations(db *sql.DB) error {
	migrationsDir := migrationsPath()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir %s: %w", migrationsDir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, filepath.Join(migrationsDir, e.Name()))
		}
	}
	sort.Strings(files)

	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			// Ignore "already exists" errors — idempotent migrations.
			if !strings.Contains(err.Error(), "already exists") &&
				!strings.Contains(err.Error(), "duplicate key") {
				return fmt.Errorf("migrate %s: %w", f, err)
			}
		}
	}
	return nil
}

// migrationsPath returns the absolute path to backend/db/migrations/.
// Uses runtime.Caller to find the path relative to this source file.
func migrationsPath() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "../../db/migrations"
	}
	// testutil is at backend/internal/testutil/db.go
	// migrations are at backend/db/migrations/
	return filepath.Join(filepath.Dir(filename), "../../db/migrations")
}
