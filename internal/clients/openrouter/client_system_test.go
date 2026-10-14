package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendChatWithSystemUsesSeparateRoles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[0].Content != "Jasmine" || request.Messages[1].Role != "user" || request.Messages[1].Content != "привет" {
			t.Errorf("wrong model roles: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ответ"}}]}`))
	}))
	defer server.Close()
	client := NewClientWithBaseURL("key", "test-model", server.URL, server.Client())
	got, err := client.SendChatWithSystem(context.Background(), "", "Jasmine", "привет")
	if err != nil || got != "ответ" {
		t.Fatalf("reply %q, err %v", got, err)
	}
}
