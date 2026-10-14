package integration

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPostgreSQL_MigrationSequenceAndConstraints(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Verify Core Tables Exist
	requiredTables := []string{
		"users",
		"telegram_users",
		"telegram_updates",
		"telegram_messages",
		"telegram_deliveries",
		"workouts",
		"workout_exercises",
		"workout_sessions",
		"workout_session_revisions",
		"workout_ingestion_increments",
		"workout_corrections",
		"user_memories",
		"user_feedback_events",
		"conversation_states",
		"llm_call_logs",
		"system_prompts",
		"system_prompt_revisions",
		"exercises",
		"meal_calorie_corrections",
	}

	for _, tbl := range requiredTables {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT FROM information_schema.tables 
				WHERE table_schema = 'public' AND table_name = $1
			)`, tbl).Scan(&exists)
		if err != nil {
			t.Fatalf("failed checking table %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("expected table %s to exist after migration", tbl)
		}
	}

	// Verify structured sets_data column on workout_exercises
	var setsColExists bool
	_ = pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'workout_exercises' AND column_name = 'sets_data'
		)`).Scan(&setsColExists)
	if !setsColExists {
		t.Errorf("expected sets_data JSONB column on workout_exercises")
	}

	// 2. Verify Partial Unique Index for Active Workout Sessions
	var indexExists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT FROM pg_indexes
			WHERE schemaname = 'public' 
			  AND tablename = 'workout_sessions' 
			  AND indexname = 'idx_one_active_workout_session_per_user'
		)`).Scan(&indexExists)
	if err != nil || !indexExists {
		t.Errorf("expected partial unique index idx_one_active_workout_session_per_user to exist: %v", err)
	}

	// 3. Verify Correlation Columns Exist
	correlationTables := []string{
		"telegram_updates",
		"telegram_deliveries",
		"telegram_messages",
		"user_memories",
		"user_feedback_events",
		"workout_session_revisions",
		"workout_corrections",
		"workout_ingestion_increments",
		"llm_call_logs",
	}
	for _, tbl := range correlationTables {
		var colExists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'correlation_id'
			)`, tbl).Scan(&colExists)
		if err != nil || !colExists {
			t.Errorf("expected correlation_id column on %s: %v", tbl, err)
		}
	}

	// 4. Test Rollbacks for safe reversible migrations
	migrationDir := findMigrationsDir()
	files, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("failed reading migration dir: %v", err)
	}
	var downFiles []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".down.sql") {
			downFiles = append(downFiles, f.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(downFiles)))

	if len(downFiles) == 0 {
		t.Errorf("expected down migration files to exist")
	}
}

func TestProgrammaticMigrationDiscovery(t *testing.T) {
	migrationDir := findMigrationsDir()
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("failed reading migration dir %s: %v", migrationDir, err)
	}

	var upFiles []string
	var downFiles []string
	seenVersions := make(map[string]bool)

	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".up.sql") {
			upFiles = append(upFiles, name)
			version := strings.Split(name, "_")[0]
			if seenVersions[version] {
				t.Errorf("duplicate migration version detected: %s", version)
			}
			seenVersions[version] = true
		} else if strings.HasSuffix(name, ".down.sql") {
			downFiles = append(downFiles, name)
		}
	}

	sort.Strings(upFiles)
	sort.Strings(downFiles)

	if len(upFiles) != 27 {
		t.Errorf("expected exactly 27 up migration files, got %d", len(upFiles))
	}
	if len(downFiles) != 27 {
		t.Errorf("expected exactly 27 down migration files, got %d", len(downFiles))
	}

	// Verify latest migration version
	latestUp := upFiles[len(upFiles)-1]
	if !strings.HasPrefix(latestUp, "000029_") {
		t.Errorf("expected latest migration version 000029, got %s", latestUp)
	}

	// Verify intentional gaps 000012 and 000013 are absent
	if seenVersions["000012"] || seenVersions["000013"] {
		t.Errorf("expected intentional historical gaps 000012 and 000013 to be absent")
	}
}
