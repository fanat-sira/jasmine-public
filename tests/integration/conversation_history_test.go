package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jasmine/internal/clients/openrouter"
	telegramclient "jasmine/internal/clients/telegram"
	"jasmine/internal/module/assistant"
	"jasmine/internal/module/telegram"
)

func TestPostgreSQL_TelegramConversationSendsLast50Turns(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username) VALUES('history') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	const chatID int64 = 77112
	for i := 1; i <= 60; i++ {
		direction := telegram.DirectionInbound
		if i%2 == 0 {
			direction = telegram.DirectionOutbound
		}
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_messages(chat_id,message_id,user_id,direction,text,created_at) VALUES($1,$2,$3,$4,$5,$6)`, chatID, i, userID, direction, fmt.Sprintf("turn-%02d", i), time.Date(2026, 9, 23, 12, 0, i, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	current := "помнишь разговор?"
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_messages(chat_id,message_id,user_id,direction,text,created_at) VALUES($1,61,$2,'inbound',$3,$4)`, chatID, userID, current, time.Date(2026, 9, 23, 12, 1, 1, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var request struct {
		SessionID string               `json:"session_id"`
		Messages  []openrouter.Message `json:"messages"`
	}
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode reply call: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "REPLY_FROM_LLM"}}}})
	}))
	defer aiServer.Close()
	botServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode send: %v", err)
		}
		if payload["text"] != "REPLY_FROM_LLM" {
			t.Errorf("unexpected Telegram text: %v", payload["text"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1001, "date": time.Now().Unix()}})
	}))
	defer botServer.Close()
	repo := telegram.NewRepository(pool)
	worker := telegram.NewDeliveryWorker(repo, telegramclient.NewClientWithBaseURL("token", botServer.URL, botServer.Client()), nil)
	client := openrouter.NewClientWithBaseURL("token", "main", aiServer.URL, aiServer.Client())
	worker.SetReplyComposer(assistant.NewTelegramReplyComposer(client, "reply"))
	payload, _ := json.Marshal(telegram.OutboxPayload{Text: "verified fact", ReplyToMessageID: ptrInt64(61)})
	delivery := &telegram.TelegramDelivery{ChatID: chatID, UserID: userID, Payload: payload, Status: telegram.DeliveryStatusPending}
	if err := repo.CreateDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if request.SessionID == "" || len(request.Messages) != 52 {
		t.Fatalf("session=%q messages=%d", request.SessionID, len(request.Messages))
	}
	if request.Messages[1].Content != "turn-11" || request.Messages[50].Content != "turn-60" {
		t.Fatalf("wrong history boundary: first=%v last=%v", request.Messages[1].Content, request.Messages[50].Content)
	}
	final, _ := request.Messages[51].Content.(string)
	if !strings.Contains(final, current) {
		t.Fatalf("current user message missing from facts: %s", final)
	}
	for _, message := range request.Messages[1:51] {
		if message.Content == current {
			t.Fatal("current user message duplicated in history")
		}
	}
	var outgoing string
	if err := pool.QueryRow(ctx, `SELECT text FROM telegram_messages WHERE chat_id=$1 AND direction='outbound' AND message_id=1001`, chatID).Scan(&outgoing); err != nil || outgoing != "REPLY_FROM_LLM" {
		t.Fatalf("persisted outbound=%q err=%v", outgoing, err)
	}
}

func ptrInt64(v int64) *int64 { return &v }
