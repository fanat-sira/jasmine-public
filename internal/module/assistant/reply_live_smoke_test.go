package assistant

import (
	"context"
	"os"
	"strings"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/telegram"
)

// Run manually with RUN_LIVE_REPLY_SMOKE=1, OPENROUTER_API_KEY and LLM_REPLY.
// It is intentionally skipped in normal CI because each case makes a paid call.
func TestLiveJasmineReplySmoke(t *testing.T) {
	if os.Getenv("RUN_LIVE_REPLY_SMOKE") != "1" {
		t.Skip("set RUN_LIVE_REPLY_SMOKE=1 for paid OpenRouter smoke test")
	}
	key, model := os.Getenv("OPENROUTER_API_KEY"), os.Getenv("LLM_REPLY")
	if key == "" || model == "" {
		t.Fatal("OPENROUTER_API_KEY and LLM_REPLY are required")
	}
	composer := NewTelegramReplyComposer(openrouter.NewClient(key, model), model)
	composer.SetPublicRuntimeInfo(PublicRuntimeInfo{Runtime: "JasmineRuntime", ReplyModel: model, HasOpenRouter: true})
	for _, message := range []string{"привет ты кто", "можешь отвечать менее официально?", "что ты о себе знаешь, тех детали"} {
		t.Run(message, func(t *testing.T) {
			answer, err := composer.Compose(context.Background(), 1, telegram.ReplyContext{UserMessage: message, EventType: "general_chat", Facts: []string{"No tool action was requested or performed."}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(answer)
			for _, forbidden := range []string{"json", "prompt injection", "инструкции не выполняю", "нормальный вопрос", "не переключаюсь"} {
				if strings.Contains(lower, forbidden) {
					t.Errorf("unexpected refusal or internal envelope reference in reply to %q: %q", message, answer)
				}
			}
			if strings.TrimSpace(answer) == "" {
				t.Fatal("empty reply")
			}
		})
	}
}
