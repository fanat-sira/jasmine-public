package integration

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"jasmine/internal/module/telegram"
)

func TestPostgreSQL_TelegramOnlyMigrationPreservesUser(t *testing.T) {
	pool, cleanup := setupTestDBThrough(t, "000027_personal_documents.up.sql")
	defer cleanup()
	ctx := context.Background()
	repo := telegram.NewRepository(pool)
	user, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 7654321})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET profile_markdown='synthetic profile' WHERE id=$1`, user.UserID); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(filepath.Join(findMigrationsDir(), "000028_telegram_only.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	again, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 7654321})
	if err != nil {
		t.Fatal(err)
	}
	if again.UserID != user.UserID {
		t.Fatal("Telegram identity changed")
	}
	var profile string
	if err := pool.QueryRow(ctx, `SELECT profile_markdown FROM users WHERE id=$1`, user.UserID).Scan(&profile); err != nil || profile != "synthetic profile" {
		t.Fatalf("profile lost: %v", err)
	}
	for _, table := range []string{"invite_codes", "refresh_tokens", "ai_generated_workouts"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("obsolete table retained: %s", table)
		}
	}
	for _, col := range []string{"email", "password_hash", "auth_provider", "is_admin"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name=$1)`, col).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("obsolete column retained: %s", col)
		}
	}
	down, err := os.ReadFile(filepath.Join(findMigrationsDir(), "000028_telegram_only.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("compatibility rollback: %v", err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("reapply Telegram-only migration: %v", err)
	}

}

func TestPostgreSQL_TelegramIdentityIndependentOfUsername(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := telegram.NewRepository(pool)
	username := "synthetic_reused_handle"
	first, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 100001, Username: &username})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 100002, Username: &username})
	if err != nil {
		t.Fatal(err)
	}
	if first.UserID == second.UserID {
		t.Fatal("mutable username shared a profile")
	}
	renamed := "different_handle"
	again, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 100001, Username: &renamed})
	if err != nil {
		t.Fatal(err)
	}
	if again.UserID != first.UserID {
		t.Fatal("renaming changed identity")
	}
	const workers = 10
	var wg sync.WaitGroup
	ids := make([]string, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 100003})
			errs[i] = err
			if u != nil {
				ids[i] = u.UserID
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		if ids[i] != ids[0] {
			t.Fatal("concurrent provisioning created multiple profiles")
		}
	}
}

func TestPostgreSQL_DisallowedTelegramUpdatesLeaveNoData(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	svc := telegram.NewService(telegram.NewRepository(pool), nil, nil, nil, nil)
	svc.SetAllowedUserIDs([]int64{123})
	for _, raw := range []string{
		`{"update_id":1,"message":{"message_id":1,"date":1,"chat":{"id":123,"type":"private"},"from":{"id":456},"text":"disallowed"}}`,
		`{"update_id":2,"message":{"message_id":2,"date":1,"chat":{"id":123,"type":"private"},"text":"missing sender"}}`,
		`{"update_id":3,"message":{"message_id":3,"date":1,"chat":{"id":-1,"type":"group"},"from":{"id":123},"text":"non-private"}}`,
	} {
		if err := svc.ProcessWebhookUpdate(ctx, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"users", "telegram_users", "telegram_updates", "telegram_messages"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("rejected update persisted %s", table)
		}
	}
}

func TestPostgreSQL_TelegramOnlyMigrationRejectsSharedProfiles(t *testing.T) {
	pool, cleanup := setupTestDBThrough(t, "000027_personal_documents.up.sql")
	defer cleanup()
	ctx := context.Background()
	repo := telegram.NewRepository(pool)
	user, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 200001})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_users(telegram_user_id,user_id) VALUES(200002,$1)`, user.UserID); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(filepath.Join(findMigrationsDir(), "000028_telegram_only.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err == nil {
		t.Fatal("ambiguous historical ownership was accepted")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('invite_codes') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("rejected migration modified schema: %v", err)
	}
}
