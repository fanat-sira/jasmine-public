package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgreSQL_ResetKeepsExerciseCatalog(t *testing.T) {
	pool, cleanup := setupTestDBThrough(t, "000027_personal_documents.up.sql")
	defer cleanup()
	ctx := context.Background()

	var userID, exerciseID, productID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, auth_provider) VALUES ('reset-user', 'telegram') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO exercises(name, exercise_code, type, muscle_group, equipment)
		VALUES ('reset_machine', 'reset_machine', 'strength', 'legs', ARRAY['machine']) RETURNING id::text`).Scan(&exerciseID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO products(name, owner_id) VALUES ('old product', $1) RETURNING id::text`, userID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO telegram_users(telegram_user_id, user_id) VALUES (123, '` + userID + `')`,
		`INSERT INTO telegram_updates(update_id, chat_id, telegram_user_id, raw_payload) VALUES (456, 123, 123, '{}')`,
		`INSERT INTO telegram_messages(chat_id, message_id, user_id, direction, text) VALUES (123, 789, '` + userID + `', 'inbound', 'old message')`,
		`INSERT INTO workouts(user_id) VALUES ('` + userID + `')`,
		`INSERT INTO meal_entries(user_id, product_id, meal_type, weight_g) VALUES ('` + userID + `', '` + productID + `', 'lunch', 100)`,
		`INSERT INTO user_memories(user_id, category, key, value) VALUES ('` + userID + `', 'preference', 'old', '{}')`,
		`INSERT INTO invite_codes(code, expires_at) VALUES ('OLD-INVITE', NOW() + INTERVAL '1 day')`,
		`INSERT INTO system_prompt_revisions(prompt_key, version, content, updated_by) VALUES ('old', 1, 'old edit', '` + userID + `')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	migration, err := os.ReadFile(filepath.Join(findMigrationsDir(), "000023_reset_legacy_user_data.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("reset migration: %v", err)
	}
	for _, table := range []string{
		"users", "telegram_users", "telegram_updates", "telegram_messages", "workouts",
		"meal_entries", "user_memories", "products", "invite_codes", "system_prompt_revisions",
	} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s still has %d rows", table, count)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM exercises WHERE id=$1`, exerciseID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exercise not preserved: count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM system_prompts`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("prompt definitions missing: count=%d err=%v", count, err)
	}
}
