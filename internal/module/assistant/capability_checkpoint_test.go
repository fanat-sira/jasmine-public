package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jasmine/internal/module/telegram"
)

func TestCapabilityIntrospectionUsesRegisteredTools(t *testing.T) {
	runtime := NewJasmineRuntime(nil, 5)
	handler := func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
		return &JasmineToolResult{}, nil
	}
	for _, spec := range []JasmineToolSpec{
		{Name: "meal.bulk_save", Skill: "meal", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update"},
		{Name: "meal.read_current", Skill: "meal", RiskLevel: RiskRead},
		{Name: "personal.read", Skill: "personal", RiskLevel: RiskRead},
	} {
		if err := runtime.Register(spec, nil, handler); err != nil {
			t.Fatal(err)
		}
	}
	info := PublicRuntimeInfo{Runtime: "JasmineRuntime"}
	for _, spec := range runtime.ToolSpecs() {
		info.Tools = append(info.Tools, spec.Name)
	}
	for _, question := range []string{"че умеешь", "ты можешь писать в бд?", "а читать бд?"} {
		if !wantsTechnicalSelfDescription(question) {
			t.Fatalf("introspection not detected: %q", question)
		}
		report, err := trustedReplyReport(telegram.ReplyContext{UserMessage: question, EventType: "general_chat"}, nil, info, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"meal.bulk_save", "meal.read_current", "personal.read"} {
			if !strings.Contains(report, tool) {
				t.Fatalf("registered capability %s missing for %q: %s", tool, question, report)
			}
		}
	}
}
