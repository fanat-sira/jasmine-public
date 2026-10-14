package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	telegramclient "jasmine/internal/clients/telegram"
	"jasmine/internal/module/prompts"
	"jasmine/internal/module/telegram"
)

type retryReply struct{ calls int }

func (r *retryReply) Compose(_ context.Context, _ int64, facts telegram.ReplyContext, _ []telegram.TelegramMessage) (string, error) {
	r.calls++
	if len(facts.Facts) != 1 || facts.Facts[0] != "verified meal saved" {
		return "", errors.New("wrong facts")
	}
	if r.calls == 1 {
		return "", errors.New("LLM_REPLY unavailable")
	}
	return "REPLY_FROM_LLM", nil
}

func TestPostgreSQL_ReplyFailureRetriesDeliveryOnly(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username) VALUES('retry') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	const chatID int64 = 555123
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_messages(chat_id,message_id,user_id,direction,text) VALUES($1,1,$2,'inbound','Завтрак')`, chatID, userID); err != nil {
		t.Fatal(err)
	}
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 2, "date": time.Now().Unix()}})
	}))
	defer server.Close()
	repo := telegram.NewRepository(pool)
	worker := telegram.NewDeliveryWorker(repo, telegramclient.NewClientWithBaseURL("token", server.URL, server.Client()), nil)
	composer := &retryReply{}
	worker.SetReplyComposer(composer)
	one := int64(1)
	payload, _ := json.Marshal(telegram.OutboxPayload{Text: "Go draft", ReplyToMessageID: &one, ReplyContext: &telegram.ReplyContext{EventType: "meal_logged", Facts: []string{"verified meal saved"}}})
	delivery := &telegram.TelegramDelivery{ChatID: chatID, UserID: userID, Payload: payload, Status: telegram.DeliveryStatusPending}
	if err := repo.CreateDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimPendingDelivery(ctx, delivery.ID, "test", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessDelivery(ctx, first); err == nil {
		t.Fatal("expected reply failure")
	}
	failed, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil || failed.Status != telegram.DeliveryStatusFailed || failed.LastError == nil {
		t.Fatalf("failed delivery=%+v err=%v", failed, err)
	}
	if sends != 0 {
		t.Fatalf("sent draft on reply failure: %d", sends)
	}
	second, err := repo.ClaimPendingDelivery(ctx, delivery.ID, "test", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessDelivery(ctx, second); err != nil {
		t.Fatal(err)
	}
	sent, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil || sent.Status != telegram.DeliveryStatusSent || sent.Attempts != 2 {
		t.Fatalf("sent delivery=%+v err=%v", sent, err)
	}
	if sends != 1 || composer.calls != 2 {
		t.Fatalf("sends=%d reply calls=%d", sends, composer.calls)
	}
	var outbound int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM telegram_messages WHERE chat_id=$1 AND direction='outbound'`, chatID).Scan(&outbound); err != nil || outbound != 1 {
		t.Fatalf("outbound=%d err=%v", outbound, err)
	}
}

func TestPostgreSQL_PromptCatalogBootstrapPreservesEdits(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := prompts.NewRepository(pool)
	if err := repo.EnsurePromptDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	for key := range prompts.Defaults {
		got, err := repo.GetPromptTemplate(ctx, key)
		if err != nil || got == "" {
			t.Fatalf("prompt %s missing: %v", key, err)
		}
	}
	const custom = "CUSTOM REPLY PERSONALITY"
	if _, err := repo.SavePrompt(ctx, prompts.KeyJasmineReply, custom); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsurePromptDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := prompts.Resolve(ctx, repo, prompts.KeyJasmineReply)
	if err != nil || got != custom {
		t.Fatalf("DB edit lost: %q %v", got, err)
	}
	// Migration 000022 seeded a short image router. Canonical bootstrap upgrades it.
	router, err := repo.GetPromptTemplate(ctx, prompts.KeyImageRouter)
	if err != nil || router != prompts.Default(prompts.KeyImageRouter) {
		t.Fatalf("historic seed not upgraded: %v", err)
	}
}

func TestPostgreSQL_PrivatePromptOverrides(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := prompts.NewRepository(pool)
	path := filepath.Join(t.TempDir(), "prompts.json")
	if err := os.WriteFile(path, []byte(`{"jasmine_reply":"PRIVATE_TEMPLATE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyOverridesFile(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsurePromptDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := prompts.Resolve(ctx, repo, prompts.KeyJasmineReply)
	if err != nil || got != "PRIVATE_TEMPLATE" {
		t.Fatal("bootstrap overwrote the private template")
	}
	if err := os.WriteFile(path, []byte(`{"jasmine_reply":"REPLACEMENT","unknown":"INVALID"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyOverridesFile(ctx, path); err == nil {
		t.Fatal("invalid override accepted")
	}
	got, err = prompts.Resolve(ctx, repo, prompts.KeyJasmineReply)
	if err != nil || got != "PRIVATE_TEMPLATE" {
		t.Fatal("invalid file partially changed the stored template")
	}
	// Valid JSON can still fail in PostgreSQL. Neither template may change.
	if err := os.WriteFile(path, []byte(`{"jasmine_reply":"REPLACEMENT","workout_generation":"\u0000"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyOverridesFile(ctx, path); err == nil {
		t.Fatal("database rejected value unexpectedly accepted")
	}
	got, err = prompts.Resolve(ctx, repo, prompts.KeyJasmineReply)
	if err != nil || got != "PRIVATE_TEMPLATE" {
		t.Fatal("failed transaction partially changed the stored template")
	}
}
