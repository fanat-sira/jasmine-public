package assistant

import (
	"jasmine/internal/clients/openrouter"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testJasmineSkill struct {
	name     string
	provider JasmineContextProvider
	tools    []JasmineTool
}

func (s testJasmineSkill) Name() string                            { return s.name }
func (s testJasmineSkill) ContextProvider() JasmineContextProvider { return s.provider }
func (s testJasmineSkill) Tools() []JasmineTool                    { return s.tools }

type testJasminePlanner func(context.Context, AssistantEvent, *JasmineContext) (JasmineStep, error)

func (p testJasminePlanner) Next(ctx context.Context, e AssistantEvent, c *JasmineContext) (JasmineStep, error) {
	return p(ctx, e, c)
}
func event() AssistantEvent {
	return AssistantEvent{ID: "turn-1", UserID: "user-1", Kind: AssistantUserMessage}
}
func readTool(skill, name string, handler JasmineToolHandler) JasmineTool {
	return JasmineTool{Spec: JasmineToolSpec{Name: name, Skill: skill, RiskLevel: RiskRead, ArgumentSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Handle: handler}
}
func TestJasmineZeroTool(t *testing.T) {
	r := NewJasmineRuntime(testJasminePlanner(func(_ context.Context, _ AssistantEvent, c *JasmineContext) (JasmineStep, error) {
		if len(c.Observations) != 0 {
			t.Fatal(c.Observations)
		}
		return JasmineStep{Final: &JasmineFinal{Text: "привет"}}, nil
	}), 5)
	got, err := r.Handle(context.Background(), event())
	if err != nil || got.ConfirmationHTML != "привет" || got.Success {
		t.Fatalf("%+v %v", got, err)
	}
	if got.ReplyContext == nil || got.ReplyContext.EventType != "general_chat" || len(got.ReplyContext.Facts) != 0 {
		t.Fatalf("planner draft must not enter LLM_REPLY: %+v", got.ReplyContext)
	}
}
func TestJasmineOneTool(t *testing.T) {
	r := NewJasmineRuntime(testJasminePlanner(func(_ context.Context, _ AssistantEvent, c *JasmineContext) (JasmineStep, error) {
		if len(c.Observations) == 0 {
			return JasmineStep{Calls: []JasmineToolCall{{Name: "a.read"}}}, nil
		}
		if c.Observations[0].Facts != "fact" {
			t.Fatal(c.Observations)
		}
		return JasmineStep{Final: &JasmineFinal{Text: "fact"}}, nil
	}), 5)
	_ = r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{readTool("a", "a.read", func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
		return &JasmineToolResult{Facts: "fact"}, nil
	})}})
	got, err := r.Handle(context.Background(), event())
	if err != nil || got.ConfirmationHTML != "fact" || got.Success {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestJasmineCrossSkillTurnAndSelectiveContext(t *testing.T) {
	loaded := map[string]int{}
	executed := []string{}
	traces := []JasmineTrace{}
	r := NewJasmineRuntime(testJasminePlanner(func(_ context.Context, _ AssistantEvent, c *JasmineContext) (JasmineStep, error) {
		switch len(c.Observations) {
		case 0:
			return JasmineStep{Call: &JasmineToolCall{Name: "a.read"}}, nil
		case 1:
			if c.Observations[0].Facts != "A" {
				t.Fatal(c.Observations)
			}
			return JasmineStep{Call: &JasmineToolCall{Name: "b.read"}}, nil
		case 2:
			if c.Observations[0].Facts != "A" || c.Observations[1].Facts != "B" {
				t.Fatal(c.Observations)
			}
			return JasmineStep{Final: &JasmineFinal{Text: "A + B"}}, nil
		}
		t.Fatal("extra planner step")
		return JasmineStep{}, nil
	}), 5)
	r.SetTraceSink(func(_ context.Context, x JasmineTrace) { traces = append(traces, x) })
	for _, skill := range []string{"a", "b", "unused"} {
		name := skill
		err := r.RegisterSkill(testJasmineSkill{name: name, provider: func(context.Context, AssistantEvent) (any, error) { loaded[name]++; return name, nil }, tools: []JasmineTool{readTool(name, name+".read", func(_ context.Context, _ AssistantEvent, c *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
			executed = append(executed, name)
			if c.SkillData[name] != name {
				return nil, fmt.Errorf("context missing")
			}
			return &JasmineToolResult{Facts: strings.ToUpper(name)}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Handle(context.Background(), event())
	if err != nil || got.ConfirmationHTML != "A + B" {
		t.Fatalf("%+v %v", got, err)
	}
	if strings.Join(executed, ",") != "a,b" || loaded["a"] != 1 || loaded["b"] != 1 || loaded["unused"] != 0 {
		t.Fatalf("executed=%v loaded=%v", executed, loaded)
	}
	if len(traces) != 6 || traces[0].Phase != "planner" || traces[1].Phase != "tool" || traces[3].Phase != "tool" || traces[5].Phase != "final" {
		t.Fatalf("trace=%+v", traces)
	}
}
func TestJasmineRejectsUnknownAndInvalidArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		call JasmineToolCall
		want string
	}{
		{"unknown", JasmineToolCall{Name: "missing"}, "unknown Jasmine tool"},
		{"malformed", JasmineToolCall{Name: "a.read", Input: []byte(`{broken`)}, "invalid"},
		{"identity", JasmineToolCall{Name: "a.read", Input: []byte(`{"user_id":"other"}`)}, "identity"},
		{"extra", JasmineToolCall{Name: "a.read", Input: []byte(`{"x":1}`)}, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewJasmineRuntime(testJasminePlanner(func(context.Context, AssistantEvent, *JasmineContext) (JasmineStep, error) {
				return JasmineStep{Call: &tc.call}, nil
			}), 5)
			_ = r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{readTool("a", "a.read", func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
				t.Fatal("executed")
				return nil, nil
			})}})
			_, err := r.Handle(context.Background(), event())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
func TestJasmineToolFailureObserved(t *testing.T) {
	r := NewJasmineRuntime(testJasminePlanner(func(_ context.Context, _ AssistantEvent, c *JasmineContext) (JasmineStep, error) {
		if len(c.Observations) == 0 {
			return JasmineStep{Call: &JasmineToolCall{Name: "a.read"}}, nil
		}
		if c.Observations[0].Success || c.Observations[0].Error != "backend unavailable" {
			t.Fatal(c.Observations)
		}
		return JasmineStep{Final: &JasmineFinal{Text: "Не получилось"}}, nil
	}), 5)
	_ = r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{readTool("a", "a.read", func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
		return nil, errors.New("backend unavailable")
	})}})
	got, err := r.Handle(context.Background(), event())
	if err != nil || got.ConfirmationHTML != "Не получилось" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestJasmineRepeatedCallAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  int
		want error
	}{{"repeat", 5, ErrJasmineRepeatedCall}, {"budget", 1, ErrJasmineRoundLimit}} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			r := NewJasmineRuntime(testJasminePlanner(func(context.Context, AssistantEvent, *JasmineContext) (JasmineStep, error) {
				return JasmineStep{Call: &JasmineToolCall{Name: "a.read"}}, nil
			}), tc.max)
			_ = r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{readTool("a", "a.read", func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
				n++
				return &JasmineToolResult{Facts: "same"}, nil
			})}})
			_, err := r.Handle(context.Background(), event())
			if !errors.Is(err, tc.want) || n != 1 {
				t.Fatalf("calls=%d err=%v", n, err)
			}
		})
	}
}
func TestJasmineWritePolicy(t *testing.T) {
	r := NewJasmineRuntime(testJasminePlanner(func(context.Context, AssistantEvent, *JasmineContext) (JasmineStep, error) {
		return JasmineStep{Call: &JasmineToolCall{Name: "a.write"}}, nil
	}), 5)
	err := r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{{Spec: JasmineToolSpec{Name: "a.write", Skill: "a", RiskLevel: RiskConfirm, RequiresConfirmation: true, IdempotencyScope: "event"}, Handle: func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
		t.Fatal("write executed")
		return nil, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Handle(context.Background(), event())
	if !errors.Is(err, ErrJasmineConfirmationRequired) {
		t.Fatal(err)
	}
}

func TestJasmineRejectsTypedArgumentAndForeignResult(t *testing.T) {
	t.Run("typed argument", func(t *testing.T) {
		r := NewJasmineRuntime(testJasminePlanner(func(context.Context, AssistantEvent, *JasmineContext) (JasmineStep, error) {
			return JasmineStep{Call: &JasmineToolCall{Name: "a.read", Input: []byte(`{"limit":"many"}`)}}, nil
		}), 5)
		err := r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{{Spec: JasmineToolSpec{Name: "a.read", Skill: "a", ArgumentSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}},"required":["limit"],"additionalProperties":false}`)}, Handle: func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
			t.Fatal("executed")
			return nil, nil
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Handle(context.Background(), event())
		if err == nil || !strings.Contains(err.Error(), "wrong type") {
			t.Fatal(err)
		}
	})
	t.Run("foreign result", func(t *testing.T) {
		r := NewJasmineRuntime(testJasminePlanner(func(_ context.Context, _ AssistantEvent, c *JasmineContext) (JasmineStep, error) {
			if len(c.Observations) == 0 {
				return JasmineStep{Call: &JasmineToolCall{Name: "a.read"}}, nil
			}
			if c.Observations[0].Success || c.Observations[0].Facts != "" || !strings.Contains(c.Observations[0].Error, "another user") {
				t.Fatal(c.Observations)
			}
			return JasmineStep{Final: &JasmineFinal{Text: "unavailable"}}, nil
		}), 5)
		_ = r.RegisterSkill(testJasmineSkill{name: "a", tools: []JasmineTool{readTool("a", "a.read", func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
			return &JasmineToolResult{OwnerUserID: "other", Facts: "private"}, nil
		})}})
		got, err := r.Handle(context.Background(), event())
		if err != nil || got.ConfirmationHTML != "unavailable" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestModelJasminePlannerUsesSharedToolCatalog(t *testing.T) {
	answers := []string{`{"calls":[{"name":"a.read","input":{}}]}`, `{"calls":[{"name":"b.read","input":{}}]}`, `{"final":"A and B"}`}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		raw := req.Messages[len(req.Messages)-1].Content
		if !strings.Contains(raw, "a.read") || !strings.Contains(raw, "b.read") {
			t.Errorf("missing shared registry: %s", raw)
		}
		if requests == 1 && !strings.Contains(raw, `"facts":"A"`) {
			t.Errorf("missing A observation: %s", raw)
		}
		if requests == 2 && !strings.Contains(raw, `"facts":"B"`) {
			t.Errorf("missing B observation: %s", raw)
		}
		answer := answers[requests]
		requests++
		encoded, _ := json.Marshal(answer)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, encoded)
	}))
	defer server.Close()
	client := openrouter.NewClientWithBaseURL("test", "model", server.URL, server.Client())
	runtime := NewJasmineRuntime(nil, 5)
	runtime.SetPlanner(NewModelJasminePlanner(client, "model", runtime.ToolSpecs))
	for _, name := range []string{"a", "b"} {
		n := name
		err := runtime.RegisterSkill(testJasmineSkill{name: n, tools: []JasmineTool{readTool(n, n+".read", func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error) {
			return &JasmineToolResult{Facts: strings.ToUpper(n)}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := runtime.Handle(context.Background(), event())
	if err != nil || got.ConfirmationHTML != "A and B" || requests != 3 {
		t.Fatalf("response=%+v err=%v requests=%d", got, err, requests)
	}
}

func TestModelJasminePlannerKeepsProposalMemoryAtomic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answer := `{"calls":[{"name":"coach.save_transient_state","input":{}},{"name":"coach.propose_workout","input":{}}]}`
		encoded, _ := json.Marshal(answer)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, encoded)
	}))
	defer server.Close()
	client := openrouter.NewClientWithBaseURL("test", "model", server.URL, server.Client())
	planner := NewModelJasminePlanner(client, "model", func() []JasmineToolSpec {
		return []JasmineToolSpec{{Name: "coach.save_transient_state", RiskLevel: RiskLowWrite}, {Name: "coach.propose_workout", RiskLevel: RiskLowWrite}}
	})
	step, err := planner.Next(context.Background(), AssistantEvent{Kind: AssistantUserMessage, Text: "ноги убитые, дай тренировку"}, &JasmineContext{})
	if err != nil || step.Call == nil || step.Call.Name != "coach.propose_workout" || len(step.Calls) != 0 {
		t.Fatalf("step=%+v err=%v", step, err)
	}
}
