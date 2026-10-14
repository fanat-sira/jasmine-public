package assistant

import (
	"jasmine/internal/module/prompts"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/pkg/correlation"
)

// ModelJasminePlanner sees the registered tool catalog, observations and bounded
// conversation context. It does not select a single skill for the whole turn.
type ModelJasminePlanner struct {
	client   *openrouter.Client
	model    string
	prompts  prompts.Store
	specs    func() []JasmineToolSpec
	fallback JasminePlanner
}

func NewModelJasminePlanner(client *openrouter.Client, model string, specs func() []JasmineToolSpec, fallback ...JasminePlanner) *ModelJasminePlanner {
	selected := JasminePlanner(CompatibilityPlanner{})
	if len(fallback) > 0 && fallback[0] != nil {
		selected = fallback[0]
	}
	return &ModelJasminePlanner{client: client, model: model, specs: specs, fallback: selected}
}

func (p *ModelJasminePlanner) SetPromptStore(store prompts.Store) { p.prompts = store }

func (p *ModelJasminePlanner) Next(ctx context.Context, e AssistantEvent, state *JasmineContext) (JasmineStep, error) {
	if p.client == nil || (e.Kind == AssistantUserPhoto || e.Kind == AssistantUserAlbum || e.Kind == AssistantUserDocument) {
		return p.fallback.Next(ctx, e, state)
	}
	if len(state.Observations) == 0 {
		if fallback, ok := p.fallback.(*CoachFallbackPlanner); ok {
			step, handled, err := fallback.SpecializedStep(ctx, e)
			if err != nil {
				return JasmineStep{}, err
			}
			if handled {
				return step, nil
			}
		}
	}
	plannerContext := make(map[string]any, len(state.SkillData))
	for name, value := range state.SkillData {
		if _, ok := value.(*CoachContext); ok {
			plannerContext[name] = map[string]any{"loaded": true}
		} else {
			plannerContext[name] = value
		}
	}
	payload, err := json.Marshal(struct {
		Event struct {
			Kind             AssistantEventKind `json:"kind"`
			Text             string             `json:"text"`
			ReplyToMessageID *int64             `json:"reply_to_message_id,omitempty"`
		} `json:"event"`
		Base         map[string]any       `json:"base_context,omitempty"`
		SkillData    map[string]any       `json:"skill_context,omitempty"`
		Observations []JasmineObservation `json:"observations,omitempty"`
		Tools        []JasmineToolSpec    `json:"tools"`
	}{Event: struct {
		Kind             AssistantEventKind `json:"kind"`
		Text             string             `json:"text"`
		ReplyToMessageID *int64             `json:"reply_to_message_id,omitempty"`
	}{e.Kind, e.Text, e.ReplyToMessageID}, Base: state.Base, SkillData: plannerContext, Observations: state.Observations, Tools: p.specs()})
	if err != nil {
		return JasmineStep{}, err
	}
	aiCtx, cancel := context.WithTimeout(openrouter.WithSessionID(correlation.WithOperation(ctx, "jasmine_planner"), conversationSessionID(e.ChatID)), 45*time.Second)
	defer cancel()
	systemPrompt, err := prompts.Resolve(ctx, p.prompts, prompts.KeyJasminePlanner)
	if err != nil {
		return JasmineStep{}, err
	}
	raw, err := p.client.SendConversation(aiCtx, p.model, systemPrompt, nil, string(payload))
	if err != nil {
		return p.fallback.Next(ctx, e, state)
	}
	var decoded struct {
		Calls []JasmineToolCall `json:"calls"`
		Final *string           `json:"final"`
	}
	if err = json.Unmarshal([]byte(stripMarkdownFences(raw)), &decoded); err != nil {
		return p.fallback.Next(ctx, e, state)
	}
	if decoded.Final != nil && len(decoded.Calls) == 0 && strings.TrimSpace(*decoded.Final) != "" {
		if fallback, ok := p.fallback.(*CoachFallbackPlanner); ok && fallback.owner != nil && fallback.owner.coachAgent != nil && e.Kind == AssistantUserMessage {
			failed := false
			for _, observation := range state.Observations {
				if !observation.Success {
					failed = true
					break
				}
			}
			if !failed {
				c, err := fallback.owner.coachAgent.build(ctx, e.UserID, e.ChatID, e.MessageID, e.ReplyToMessageID)
				if err != nil {
					return JasmineStep{}, err
				}
				candidate, err := fallbackCoachAction(e, c)
				if err != nil {
					return JasmineStep{}, err
				}
				if candidate.Call != nil {
					for _, spec := range p.specs() {
						if spec.Name == candidate.Call.Name && spec.RiskLevel == RiskLowWrite {
							return candidate, nil
						}
					}
				}
			}
		}
		return JasmineStep{Final: &JasmineFinal{Text: *decoded.Final, EventType: "planner_result"}}, nil
	}
	if len(decoded.Calls) == 0 || len(decoded.Calls) > 5 || decoded.Final != nil {
		return p.fallback.Next(ctx, e, state)
	}
	for _, call := range decoded.Calls {
		if call.Name == "" {
			return JasmineStep{}, fmt.Errorf("planner returned empty tool name")
		}
	}
	if len(decoded.Calls) > 1 {
		last := decoded.Calls[len(decoded.Calls)-1]
		if last.Name == "coach.propose_workout" {
			proposalCombo := true
			for _, call := range decoded.Calls[:len(decoded.Calls)-1] {
				if call.Name != "coach.save_memory" && call.Name != "coach.save_transient_state" {
					proposalCombo = false
					break
				}
			}
			if proposalCombo {
				return JasmineStep{Call: &last}, nil
			} // proposal tool saves these in its transaction
		}
		mutating := map[string]bool{}
		for _, spec := range p.specs() {
			if spec.RiskLevel != RiskRead {
				mutating[spec.Name] = true
			}
		}
		count := 0
		for i, call := range decoded.Calls {
			if mutating[call.Name] {
				count++
				if i != len(decoded.Calls)-1 {
					return p.fallback.Next(ctx, e, state)
				}
			}
		}
		if count > 1 {
			return p.fallback.Next(ctx, e, state)
		}
	}
	return JasmineStep{Calls: decoded.Calls}, nil
}
