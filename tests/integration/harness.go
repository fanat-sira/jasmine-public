package integration

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GetTestDatabaseURL retrieves the Postgres connection string from environment.
func GetTestDatabaseURL(t *testing.T) string {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "1" {
		t.Skip("RUN_INTEGRATION_TESTS is not enabled; skipping live PostgreSQL integration tests")
	}
	url := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if url == "" {
		t.Fatalf("FATAL: RUN_INTEGRATION_TESTS=1 is set, but TEST_DATABASE_URL is empty; failing to prevent false-green integration run")
	}
	return url
}

// SetupTestDB initializes a clean PostgreSQL database schema and executes all up migrations.
func SetupTestDB(t *testing.T) (*pgxpool.Pool, func()) { return setupTestDBThrough(t, "") }

func setupTestDBThrough(t *testing.T, lastMigration string) (*pgxpool.Pool, func()) {
	t.Helper()
	dbURL := GetTestDatabaseURL(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("failed to parse TEST_DATABASE_URL: %v", err)
	}
	config.MaxConns = 25

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("failed to connect to PostgreSQL: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping PostgreSQL: %v", err)
	}

	// 1. Reset public schema cleanly
	_, err = pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`)
	if err != nil {
		t.Fatalf("failed to reset public schema: %v", err)
	}

	// 2. Apply all up migrations sequentially
	migrationDir := findMigrationsDir()
	files, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("failed to read migrations directory %s: %v", migrationDir, err)
	}

	var upFiles []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".up.sql") && (lastMigration == "" || f.Name() <= lastMigration) {
			upFiles = append(upFiles, f.Name())
		}
	}
	sort.Strings(upFiles)

	for _, fileName := range upFiles {
		fullPath := filepath.Join(migrationDir, fileName)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("failed to read migration file %s: %v", fileName, err)
		}
		if _, err := pool.Exec(ctx, string(content)); err != nil {
			t.Fatalf("failed to execute migration %s: %v", fileName, err)
		}
	}

	cleanup := func() {
		pool.Close()
	}

	return pool, cleanup
}

func findMigrationsDir() string {
	candidates := []string{
		"../../scripts/migrations",
		"../scripts/migrations",
		"./scripts/migrations",
		"scripts/migrations",
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return "./scripts/migrations"
}
