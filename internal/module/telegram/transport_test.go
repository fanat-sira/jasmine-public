package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	telegramclient "jasmine/internal/clients/telegram"
)

func TestStartWebhookRegistersConfiguredEndpoint(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setWebhook") {
			t.Errorf("unexpected Telegram method %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode webhook request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	client := telegramclient.NewClientWithBaseURL("test-token", server.URL, server.Client())
	service := &Service{client: client}
	service.StartWebhook(context.Background(), "https://bot.example/api/v1/telegram/webhook", "secret")
	if got["url"] != "https://bot.example/api/v1/telegram/webhook" || got["secret_token"] != "secret" {
		t.Fatalf("wrong webhook registration: %+v", got)
	}
}

func TestStartPollingRemovesWebhookAndAdvancesOffset(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	var offsets []float64
	secondPoll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "deleteWebhook" {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		var body struct {
			Offset float64 `json:"offset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		offsets = append(offsets, body.Offset)
		mu.Unlock()
		if body.Offset == 0 {
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":7}]}`))
			return
		}
		select {
		case <-secondPoll:
		default:
			close(secondPoll)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()
	client := telegramclient.NewClientWithBaseURL("test-token", server.URL, server.Client())
	service := &Service{client: client}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.StartPolling(ctx); close(done) }()
	select {
	case <-secondPoll:
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not advance to the next update")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) < 3 || methods[0] != "deleteWebhook" || len(offsets) < 2 || offsets[0] != 0 || offsets[1] != 8 {
		t.Fatalf("wrong polling lifecycle: methods=%v offsets=%v", methods, offsets)
	}
}
