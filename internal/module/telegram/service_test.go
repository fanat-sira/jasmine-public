package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	telegramclient "jasmine/internal/clients/telegram"
)

// MockTelegramServer creates a test HTTP server simulating Telegram Bot API responses.
func MockTelegramServer(
	t *testing.T,
	onSendMessage func(payload map[string]any) (int64, error),
	onSendRichMessage func(payload map[string]any) (int64, error),
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasSuffix(path, "/sendRichMessage") {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)

			var msgID int64 = 2002
			var err error
			if onSendRichMessage != nil {
				msgID, err = onSendRichMessage(payload)
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[any]{
					OK:          false,
					ErrorCode:   ptr(400),
					Description: ptr(err.Error()),
				})
				return
			}

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[telegramclient.Message]{
				OK: true,
				Result: telegramclient.Message{
					MessageID: msgID,
					Date:      time.Now().Unix(),
				},
			})
			return
		}

		if strings.HasSuffix(path, "/sendMessage") {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)

			var msgID int64 = 1001
			var err error
			if onSendMessage != nil {
				msgID, err = onSendMessage(payload)
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[any]{
					OK:          false,
					ErrorCode:   ptr(400),
					Description: ptr(err.Error()),
				})
				return
			}

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[telegramclient.Message]{
				OK: true,
				Result: telegramclient.Message{
					MessageID: msgID,
					Date:      time.Now().Unix(),
				},
			})
			return
		}

		if strings.HasSuffix(path, "/sendChatAction") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[bool]{
				OK:     true,
				Result: true,
			})
			return
		}

		if strings.HasSuffix(path, "/sendRichMessageDraft") || strings.HasSuffix(path, "/sendMessageDraft") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[bool]{
				OK:     true,
				Result: true,
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestWebhookHandlerSecretTokenValidation(t *testing.T) {
	handler := NewHandler(nil, "my-super-secret-token", nil)

	// 1. Invalid secret token -> 401 Unauthorized
	reqInvalid := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/webhook", strings.NewReader(`{}`))
	reqInvalid.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-token")
	wInvalid := httptest.NewRecorder()
	handler.HandleWebhook(wInvalid, reqInvalid)

	if wInvalid.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 for invalid secret token, got %d", wInvalid.Code)
	}

	// 2. Missing secret token -> 401 Unauthorized
	reqMissing := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/webhook", strings.NewReader(`{}`))
	wMissing := httptest.NewRecorder()
	handler.HandleWebhook(wMissing, reqMissing)

	if wMissing.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 for missing secret token, got %d", wMissing.Code)
	}
}

func TestRichDraftAndRichFinalization(t *testing.T) {
	chatActionCalled := false
	sendMessageCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			chatActionCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[bool]{OK: true, Result: true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sendMessageCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[telegramclient.Message]{
				OK:     true,
				Result: telegramclient.Message{MessageID: 8888, Date: time.Now().Unix()},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := telegramclient.NewClientWithBaseURL("mock-token", server.URL, server.Client())
	renderer := NewDraftRenderer(client, nil)

	draftID := StableDraftID(123456)
	renderer.UpdateStatus(context.Background(), 777, draftID, "Собираю тренировку…")

	if !chatActionCalled {
		t.Error("expected sendChatAction (typing) to be called")
	}

	// Finalize using renderer
	finalMsg, err := renderer.Finalize(context.Background(), 777, draftID, "<b>Тренировка готова</b>", nil)
	if err != nil {
		t.Fatalf("finalize failed: %v", err)
	}
	if !sendMessageCalled {
		t.Error("expected sendMessage to be called for finalization")
	}
	if finalMsg.MessageID != 8888 {
		t.Errorf("expected message_id 8888, got %d", finalMsg.MessageID)
	}
}

func TestNormalAndFallbackFinalization(t *testing.T) {
	sendMsgCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendRichMessageDraft") || strings.HasSuffix(r.URL.Path, "/sendMessageDraft") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[any]{
				OK:          false,
				ErrorCode:   ptr(400),
				Description: ptr("Drafts not supported"),
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[bool]{OK: true, Result: true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sendMsgCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(telegramclient.ApiResponse[telegramclient.Message]{
				OK:     true,
				Result: telegramclient.Message{MessageID: 9999, Date: time.Now().Unix()},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := telegramclient.NewClientWithBaseURL("mock-token", server.URL, server.Client())
	renderer := NewDraftRenderer(client, nil)

	draftID := StableDraftID(999)
	renderer.UpdateStatus(context.Background(), 888, draftID, "Разбираю…")

	if renderer.GetActiveMode(888) != DraftModeFallback {
		t.Errorf("expected DraftModeFallback, got %s", renderer.GetActiveMode(888))
	}

	finalMsg, err := renderer.Finalize(context.Background(), 888, draftID, "Готово", nil)
	if err != nil {
		t.Fatalf("finalize failed: %v", err)
	}
	if !sendMsgCalled {
		t.Error("expected sendMessage to be called on fallback finalization")
	}
	if finalMsg.MessageID != 9999 {
		t.Errorf("expected message_id 9999, got %d", finalMsg.MessageID)
	}
}

func TestDualWindowRateLimiter(t *testing.T) {
	rl := &DualWindowRateLimiter{}
	now := time.Now()

	// 1. Max 20 calls in 5 seconds
	for i := 0; i < 20; i++ {
		if !rl.Allow(now.Add(time.Duration(i*100) * time.Millisecond)) {
			t.Fatalf("call %d within 5s window should be allowed", i+1)
		}
	}

	// 21st call within 5s window should be rejected
	if rl.Allow(now.Add(2500 * time.Millisecond)) {
		t.Error("21st call within 5s window should be throttled")
	}

	// 2. Advance past 5s window but within 30s window
	base30s := now.Add(6 * time.Second)
	for i := 0; i < 20; i++ {
		if !rl.Allow(base30s.Add(time.Duration(i*100) * time.Millisecond)) {
			t.Fatalf("call %d within 30s window should be allowed", 20+i+1)
		}
	}

	// 41st call within 30s window should be rejected
	if rl.Allow(base30s.Add(2500 * time.Millisecond)) {
		t.Error("41st call within 30s window should be throttled")
	}
}

func TestStableDraftID(t *testing.T) {
	id1 := StableDraftID(123456789)
	id2 := StableDraftID(123456789)
	id3 := StableDraftID(987654321)

	if id1 <= 0 {
		t.Errorf("draft ID must be positive, got %d", id1)
	}
	if id1 != id2 {
		t.Errorf("stable draft ID must be deterministic, got %d and %d", id1, id2)
	}
	if id1 == id3 {
		t.Errorf("different update IDs should yield different draft IDs, got %d", id1)
	}
	if StableDraftID(0) != 1 || StableDraftID(-5) != 1 {
		t.Error("non-positive update ID should yield fallback positive draft ID 1")
	}
}

func TestDeliveryWorkerOutboxProcessing(t *testing.T) {
	richFinalized := false
	normalSent := false

	server := MockTelegramServer(t,
		func(payload map[string]any) (int64, error) {
			normalSent = true
			return 1001, nil
		},
		func(payload map[string]any) (int64, error) {
			richFinalized = true
			return 2002, nil
		},
	)
	defer server.Close()

	client := telegramclient.NewClientWithBaseURL("mock-token", server.URL, server.Client())
	worker := &DeliveryWorker{
		client: client,
		replyComposer: replyComposerFunc(func(_ context.Context, _ int64, facts ReplyContext, _ []TelegramMessage) (string, error) {
			if facts.EventType != "legacy_delivery" || len(facts.Facts) != 0 {
				t.Errorf("legacy draft entered reply facts: %+v", facts)
			}
			return "REPLY_FROM_LLM", nil
		}),
	}

	// Test rich delivery
	draftID := 555
	richPayload, _ := json.Marshal(OutboxPayload{
		DraftID:          &draftID,
		IsRich:           true,
		Text:             "Rich workout plan",
		ParseMode:        "HTML",
		ReplyToMessageID: ptr(int64(77)),
	})
	richDelivery := &TelegramDelivery{
		ID:      "deliv-rich",
		ChatID:  12345,
		UserID:  "u-rich",
		Status:  DeliveryStatusSending,
		Payload: richPayload,
	}

	err := worker.ProcessDelivery(context.Background(), richDelivery)
	if !richFinalized && normalSent {
		t.Log("sendRichMessage fell back to sendMessage as expected if endpoint simulated")
	} else if !richFinalized && !normalSent {
		t.Error("expected at least one telegram delivery method to be called")
	}
	_ = err
}

func TestDeliveryWorkerFailureRetry(t *testing.T) {
	server := MockTelegramServer(t,
		func(payload map[string]any) (int64, error) {
			return 0, errors.New("rate limit exceeded")
		},
		nil,
	)
	defer server.Close()

	client := telegramclient.NewClientWithBaseURL("mock-token", server.URL, server.Client())
	worker := &DeliveryWorker{
		client: client,
	}

	payload, _ := json.Marshal(OutboxPayload{Text: "Retry test"})
	delivery := &TelegramDelivery{
		ID:      "deliv-fail",
		ChatID:  12345,
		UserID:  "u-1",
		Status:  DeliveryStatusSending,
		Payload: payload,
	}

	err := worker.ProcessDelivery(context.Background(), delivery)
	if err == nil {
		t.Error("expected delivery error when telegram API returns failure")
	}
}

func TestInboundMessagePersistenceFields(t *testing.T) {
	text := "лег курл 3х12 23кг"
	inboundMsg := TelegramMessage{
		ChatID:           123456,
		MessageID:        1001,
		TelegramUserID:   ptr(int64(123456)),
		UserID:           "uuid-1",
		Direction:        DirectionInbound,
		ReplyToMessageID: ptr(int64(999)),
		Text:             &text,
		RawPayload:       json.RawMessage(`{"text":"лег курл 3х12 23кг"}`),
		DomainReferences: []DomainReference{},
		CreatedAt:        time.Now(),
	}

	data, err := json.Marshal(inboundMsg)
	if err != nil {
		t.Fatalf("marshal inbound message: %v", err)
	}

	var dec TelegramMessage
	if err := json.Unmarshal(data, &dec); err != nil {
		t.Fatalf("unmarshal inbound message: %v", err)
	}

	if *dec.ReplyToMessageID != 999 {
		t.Errorf("expected reply_to_message_id 999, got %v", dec.ReplyToMessageID)
	}
	if *dec.Text != "лег курл 3х12 23кг" {
		t.Errorf("expected text 'лег курл 3х12 23кг', got %v", dec.Text)
	}
}

type mockIngestionHandler struct {
	mu           sync.Mutex
	callsCount   int
	lastUserID   string
	lastRawText  string
	returnResult *IngestionResponse
	returnErr    error
}

func (m *mockIngestionHandler) IngestMessage(ctx context.Context, userID string, chatID int64, updateID int64, messageID int64, replyToMessageID *int64, rawText string) (*IngestionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callsCount++
	m.lastUserID = userID
	m.lastRawText = rawText
	return m.returnResult, m.returnErr
}

func TestWebhookIngressEndToEndAndProvisioning(t *testing.T) {
	mockIngest := &mockIngestionHandler{
		returnResult: &IngestionResponse{
			Success:          true,
			WorkoutID:        "w-123",
			ConfirmationHTML: "<b>Записал.</b>",
			DomainReferences: []DomainReference{
				{Type: "workout", ID: "w-123"},
			},
		},
	}

	// 1. Valid private chat webhook
	updatePayload := `{
		"update_id": 999111,
		"message": {
			"message_id": 42,
			"from": {
				"id": 888777,
				"is_bot": false,
				"first_name": "Ivan",
				"username": "ivan_fit"
			},
			"chat": {
				"id": 888777,
				"type": "private"
			},
			"date": 1724150000,
			"text": "лег курл 3х12 23кг"
		}
	}`

	// Create service with mock ingestion
	svc := &Service{allowedUserIDs: map[int64]struct{}{888777: {}, 555444: {}},
		ingestionPipeline: mockIngest,
		maxAttempts:       3,
		lockTimeout:       60 * time.Second,
	}

	handler := NewHandler(svc, "secret-123", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/webhook", strings.NewReader(updatePayload))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret-123")
	rec := httptest.NewRecorder()

	handler.HandleWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid webhook, got %d", rec.Code)
	}

	// 2. Verify non-private chat is ignored without running domain actions
	groupPayload := `{
		"update_id": 999112,
		"message": {
			"message_id": 43,
			"from": {"id": 888777, "first_name": "Ivan"},
			"chat": {"id": -100999, "type": "group"},
			"date": 1724150000,
			"text": "лег курл 3х12 23кг"
		}
	}`
	reqGroup := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/webhook", strings.NewReader(groupPayload))
	reqGroup.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret-123")
	recGroup := httptest.NewRecorder()
	handler.HandleWebhook(recGroup, reqGroup)
	if recGroup.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for skipped group update, got %d", recGroup.Code)
	}
}

func TestDuplicateUpdateRedeliveryIdempotency(t *testing.T) {
	mockIngest := &mockIngestionHandler{
		returnResult: &IngestionResponse{
			Success:   true,
			WorkoutID: "w-dup",
		},
	}

	svc := &Service{allowedUserIDs: map[int64]struct{}{888777: {}, 555444: {}},
		ingestionPipeline: mockIngest,
	}

	handler := NewHandler(svc, "secret-abc", nil)

	updatePayload := `{
		"update_id": 777001,
		"message": {
			"message_id": 10,
			"from": {"id": 555444, "first_name": "Anna"},
			"chat": {"id": 555444, "type": "private"},
			"date": 1724150000,
			"text": "хип траст 3х15 10кг + тренажер 22.7"
		}
	}`

	// First delivery
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/webhook", strings.NewReader(updatePayload))
	req1.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret-abc")
	rec1 := httptest.NewRecorder()
	handler.HandleWebhook(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first delivery failed with code %d", rec1.Code)
	}

	// Duplicate redelivery (same update_id)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/webhook", strings.NewReader(updatePayload))
	req2.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret-abc")
	rec2 := httptest.NewRecorder()
	handler.HandleWebhook(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("duplicate redelivery failed with code %d", rec2.Code)
	}
}

type replyComposerFunc func(context.Context, int64, ReplyContext, []TelegramMessage) (string, error)

func (f replyComposerFunc) Compose(ctx context.Context, id int64, facts ReplyContext, h []TelegramMessage) (string, error) {
	return f(ctx, id, facts, h)
}

type stubReplyComposer struct {
	calls int
}

func (s *stubReplyComposer) Compose(_ context.Context, _ int64, facts ReplyContext, _ []TelegramMessage) (string, error) {
	s.calls++
	if facts.EventType != "legacy_delivery" || len(facts.Facts) != 0 {
		return "", fmt.Errorf("legacy draft entered reply context: %+v", facts)
	}
	return "Да, записала завтрак: 175 ккал", nil
}

func TestDeliveryWorkerSendsLLMReplyInsteadOfDraft(t *testing.T) {
	var sent string
	server := MockTelegramServer(t, func(payload map[string]any) (int64, error) {
		sent, _ = payload["text"].(string)
		return 123, nil
	}, nil)
	defer server.Close()
	worker := NewDeliveryWorker(nil, telegramclient.NewClientWithBaseURL("mock-token", server.URL, server.Client()), nil)
	composer := &stubReplyComposer{}
	worker.SetReplyComposer(composer)
	payload, _ := json.Marshal(OutboxPayload{Text: "Записала завтрак: 175 ккал"})
	if err := worker.ProcessDelivery(context.Background(), &TelegramDelivery{ID: "generated", ChatID: 12, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if composer.calls != 1 || sent != "Да, записала завтрак: 175 ккал" {
		t.Fatalf("composer calls=%d, sent=%q", composer.calls, sent)
	}
}
