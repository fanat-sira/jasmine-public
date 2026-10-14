package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOutboundTelegramTextIsNormalized(t *testing.T) {
	const input = `<b>«Ёж» — “всё”</b>`
	const want = `<b>"Еж" - "все"</b>`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Text   string              `json:"text"`
			Blocks []RichBlockThinking `json:"blocks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		got := body.Text
		if strings.HasSuffix(r.URL.Path, "sendRichMessageDraft") {
			if len(body.Blocks) != 1 {
				t.Errorf("rich draft blocks: %+v", body.Blocks)
			} else {
				got = body.Blocks[0].Text
			}
		}
		if got != want {
			t.Errorf("%s sent %q, want %q", r.URL.Path, got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "Draft") {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		} else {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1}}}`))
		}
	}))
	defer server.Close()
	client := NewClientWithBaseURL("token", server.URL, server.Client())
	ctx := context.Background()
	if _, err := client.SendMessage(ctx, 1, input, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendRichMessage(ctx, 1, 2, input, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.SendMessageDraft(ctx, 1, 2, input); err != nil {
		t.Fatal(err)
	}
	if err := client.SendRichMessageDraft(ctx, 1, 2, []RichBlockThinking{{Type: "thinking", Text: input}}); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("saw %d Telegram sends, want 4", calls)
	}
}
