package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	telegramclient "jasmine/internal/clients/telegram"
)

func TestReplyBoundaryForAlphaFlows(t *testing.T) {
	flows := []string{
		"greeting", "general", "workout_proposal", "set_logged", "correction",
		"workout_clarification", "workout_finish", "meal_photo", "scale_photo",
		"gym_machine_clarification", "unknown_image", "profile_response",
	}
	for _, flow := range flows {
		t.Run(flow, func(t *testing.T) {
			var sent string
			server := MockTelegramServer(t, func(payload map[string]any) (int64, error) {
				sent, _ = payload["text"].(string)
				return 100, nil
			}, nil)
			defer server.Close()
			worker := NewDeliveryWorker(nil, telegramclient.NewClientWithBaseURL("token", server.URL, server.Client()), nil)
			calls := 0
			worker.SetReplyComposer(replyComposerFunc(func(_ context.Context, _ int64, facts ReplyContext, _ []TelegramMessage) (string, error) {
				calls++
				if facts.EventType != flow || len(facts.Facts) != 1 || facts.Facts[0] != "verified fact" {
					t.Fatalf("bad facts: %+v", facts)
				}
				return "REPLY_FROM_LLM", nil
			}))
			payload, _ := json.Marshal(OutboxPayload{Text: "hardcoded domain draft", ReplyContext: &ReplyContext{EventType: flow, Facts: []string{"verified fact"}}})
			if err := worker.ProcessDelivery(context.Background(), &TelegramDelivery{ID: flow, ChatID: 1, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || sent != "REPLY_FROM_LLM" {
				t.Fatalf("calls=%d sent=%q", calls, sent)
			}
		})
	}
}

func TestReplyFailureNeverSendsDraft(t *testing.T) {
	cases := []struct {
		name     string
		composer ReplyComposer
	}{
		{"missing", nil},
		{"error", replyComposerFunc(func(context.Context, int64, ReplyContext, []TelegramMessage) (string, error) {
			return "", errors.New("model unavailable")
		})},
		{"empty", replyComposerFunc(func(context.Context, int64, ReplyContext, []TelegramMessage) (string, error) { return "   ", nil })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sends := 0
			server := MockTelegramServer(t, func(map[string]any) (int64, error) { sends++; return 100, nil }, nil)
			defer server.Close()
			worker := NewDeliveryWorker(nil, telegramclient.NewClientWithBaseURL("token", server.URL, server.Client()), nil)
			worker.SetReplyComposer(tc.composer)
			payload, _ := json.Marshal(map[string]any{"text": "Go-generated sentence", "generated_reply": true})
			err := worker.ProcessDelivery(context.Background(), &TelegramDelivery{ID: fmt.Sprint(tc.name), ChatID: 1, Payload: payload})
			if err == nil || sends != 0 {
				t.Fatalf("err=%v sends=%d", err, sends)
			}
		})
	}
}
