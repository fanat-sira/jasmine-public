package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramAllowlistRejectsBeforeProcessing(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil)
	svc.SetAllowedUserIDs([]int64{123})
	updates := []string{
		`{"update_id":1,"message":{"message_id":2,"date":1,"chat":{"id":456,"type":"private"},"from":{"id":456},"text":"hello"}}`,
		`{"update_id":2,"message":{"message_id":3,"date":1,"chat":{"id":123,"type":"private"},"text":"hello"}}`,
	}
	for _, raw := range updates {
		if err := svc.ProcessWebhookUpdate(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("rejected update returned error: %v", err)
		}
	}
}

func TestEmptyTelegramAllowlistDeniesAll(t *testing.T) {
	for _, ids := range [][]int64{nil, {}} {
		svc := NewService(nil, nil, nil, nil, nil)
		svc.SetAllowedUserIDs(ids)
		recorder := &mockIngestionHandler{}
		svc.SetIngestionPipeline(recorder)
		raw := `{"update_id":1,"message":{"message_id":2,"date":1,"chat":{"id":123,"type":"private"},"from":{"id":123},"text":"hello"}}`
		if err := svc.ProcessWebhookUpdate(context.Background(), []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if recorder.callsCount != 0 {
			t.Fatal("empty allowlist permitted ingestion")
		}
	}
}

func TestWebhookWithoutSecretFailsClosed(t *testing.T) {
	handler := NewHandler(nil, "", nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/telegram/webhook", strings.NewReader(`{}`))
	handler.HandleWebhook(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured webhook returned %d", w.Code)
	}
}
