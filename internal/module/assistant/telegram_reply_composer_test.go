package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/telegram"
)

func TestTelegramReplyComposerUsesReplyModelAndHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string               `json:"model"`
			SessionID string               `json:"session_id"`
			Messages  []openrouter.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "reply-model" || req.SessionID != conversationSessionID(12) {
			t.Errorf("wrong routing: model=%q session=%q", req.Model, req.SessionID)
		}
		if len(req.Messages) != 4 || req.Messages[1].Content != "Завтрак" || req.Messages[2].Content != "записала" {
			t.Errorf("missing conversation: %+v", req.Messages)
		}
		if !strings.Contains(req.Messages[0].Content.(string), "175 ккал") || req.Messages[3].Content != "Завтрак" {
			t.Errorf("facts/user boundary is wrong: %+v", req.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "Записала: 175 ккал <ок>"}}}})
	}))
	defer server.Close()
	client := openrouter.NewClientWithBaseURL("test", "main-model", server.URL, server.Client())
	composer := NewTelegramReplyComposer(client, "reply-model")
	inbound, outbound := "Завтрак", "записала"
	answer, err := composer.Compose(context.Background(), 12, telegram.ReplyContext{UserMessage: "Завтрак", EventType: "test", Facts: []string{"175 ккал"}}, []telegram.TelegramMessage{{Direction: telegram.DirectionInbound, Text: &inbound}, {Direction: telegram.DirectionOutbound, Text: &outbound}})
	if err != nil || answer != "Записала: 175 ккал &lt;ок&gt;" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}
