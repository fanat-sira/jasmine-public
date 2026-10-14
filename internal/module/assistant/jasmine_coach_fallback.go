package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"jasmine/internal/module/telegram"
)

// CoachFallbackPlanner is a deterministic fallback for LLM failure. It never
// routes an ordinary workout turn to the message-level Orchestrator.
type CoachFallbackPlanner struct{ owner *Orchestrator }

func NewCoachFallbackPlanner(owner *Orchestrator) *CoachFallbackPlanner {
	return &CoachFallbackPlanner{owner: owner}
}

// SpecializedStep preserves existing profile, nutrition and pending-image
// precedence before a model can misclassify the turn as a workout action.
func (p *CoachFallbackPlanner) SpecializedStep(ctx context.Context, e AssistantEvent) (JasmineStep, bool, error) {
	if p == nil || p.owner == nil || e.Kind != AssistantUserMessage {
		return JasmineStep{}, false, nil
	}
	basic, err := isSpecializedText(ctx, p.owner, e, nil)
	if err != nil {
		return JasmineStep{}, false, err
	}
	if basic {
		return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_specialized_text"}}, true, nil
	}
	if call, ok := fallbackMealAction(e.Text); ok {
		return JasmineStep{Call: call}, true, nil
	}
	if wantsPersonalIntrospection(e.Text) {
		if strings.Contains(strings.ToLower(e.Text), "проект") {
			return JasmineStep{Call: &JasmineToolCall{Name: "personal.list_projects"}}, true, nil
		}
		return JasmineStep{Call: &JasmineToolCall{Name: "personal.read"}}, true, nil
	}
	if p.owner.telegramRepo != nil {
		pending, err := p.owner.telegramRepo.GetConversationState(ctx, e.UserID)
		if err != nil && err != telegram.ErrNotFound {
			return JasmineStep{}, false, err
		}
		if pending != nil && pendingMatches(pending.PendingClarification, e.Text, e.ReplyToMessageID) {
			if pending.PendingClarification.Type == "machine_display_needs_machine" {
				input, _ := json.Marshal(map[string]string{"machine_name": strings.TrimSpace(e.Text)})
				return JasmineStep{Call: &JasmineToolCall{Name: "coach.log_machine_metrics", Input: input}}, true, nil
			}
			switch pending.PendingClarification.Type {
			case "image_unknown_needs_intent":
				return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_specialized_text"}}, true, nil
			case "exercise_disambiguation", "correction_disambiguation":
				return JasmineStep{Call: &JasmineToolCall{Name: "coach.continue_workout_clarification"}}, true, nil
			}
		}
	}
	if p.owner.coachAgent != nil && (wantsLogLabelMeal(e.Text) || isMealCorrectionAffirmation(e.Text) || labelCaptionGrams.MatchString(e.Text)) {
		c, err := p.owner.coachAgent.build(ctx, e.UserID, e.ChatID, e.MessageID, e.ReplyToMessageID)
		if err != nil {
			return JasmineStep{}, false, err
		}
		specialized, err := isSpecializedText(ctx, p.owner, e, c)
		if err != nil {
			return JasmineStep{}, false, err
		}
		if specialized {
			return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_specialized_text"}}, true, nil
		}
	}
	return JasmineStep{}, false, nil
}

func (p *CoachFallbackPlanner) Next(ctx context.Context, e AssistantEvent, state *JasmineContext) (JasmineStep, error) {
	if len(state.Observations) > 0 {
		last := state.Observations[len(state.Observations)-1]
		if last.Success {
			return JasmineStep{Final: &JasmineFinal{EventType: "tool_result"}}, nil
		}
		return JasmineStep{Final: &JasmineFinal{EventType: "tool_failed"}}, nil
	}
	if e.Kind == AssistantUserPhoto {
		return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_photo"}}, nil
	}
	if e.Kind == AssistantUserDocument {
		return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_document"}}, nil
	}
	if e.Kind == AssistantUserAlbum {
		return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_album"}}, nil
	}
	if e.Kind != AssistantUserMessage {
		return JasmineStep{}, fmt.Errorf("unsupported event %s", e.Kind)
	}
	if p.owner == nil || p.owner.coachAgent == nil {
		return JasmineStep{}, fmt.Errorf("Coach is unavailable")
	}
	if step, handled, err := p.SpecializedStep(ctx, e); err != nil {
		return JasmineStep{}, err
	} else if handled {
		return step, nil
	}
	c, err := p.owner.coachAgent.build(ctx, e.UserID, e.ChatID, e.MessageID, e.ReplyToMessageID)
	if err != nil {
		return JasmineStep{}, err
	}
	c.TurnText = e.Text
	return fallbackCoachAction(e, c)
}

func fallbackCoachAction(e AssistantEvent, c *CoachContext) (JasmineStep, error) {
	lower := strings.ToLower(strings.TrimSpace(e.Text))
	if c != nil && c.Session != nil && strings.Contains(lower, "почему") {
		return JasmineStep{Call: &JasmineToolCall{Name: "coach.fitness_question"}}, nil
	}
	if isLegacyBulkWorkoutReport(e.Text) {
		return JasmineStep{Call: &JasmineToolCall{Name: "coach.log_exercise"}}, nil
	}
	plan := ruleCoachPlan(CoachTurn{Text: e.Text, ReplyToMessageID: e.ReplyToMessageID}, c)
	if plan == nil || len(plan.Calls) == 0 {
		return JasmineStep{Final: &JasmineFinal{EventType: "clarification_needed"}}, nil
	}
	call := plan.Calls[0]
	for _, candidate := range plan.Calls {
		if candidate.Name == "propose_workout" {
			call = candidate
			break
		}
	}
	if call.Name == "general_chat" {
		return JasmineStep{Final: &JasmineFinal{EventType: "general_chat"}}, nil
	}
	input, err := coachToolInput(call)
	if err != nil {
		return JasmineStep{}, err
	}
	return JasmineStep{Call: &JasmineToolCall{Name: "coach." + call.Name, Input: input}}, nil
}
func coachToolInput(call CoachToolCall) (json.RawMessage, error) {
	b, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	delete(fields, "name")
	return json.Marshal(fields)
}
func isSpecializedText(ctx context.Context, o *Orchestrator, e AssistantEvent, c *CoachContext) (bool, error) {
	if _, ok := profileMarkdownText(e.Text); ok {
		return true, nil
	}
	if _, ok := profileJSONText(e.Text); ok {
		return true, nil
	}
	if isDailyMealsQuestion(e.Text) || isMealStatusQuestion(e.Text) {
		return true, nil
	}
	if _, ok, err := o.mealCorrectionRequest(ctx, e.ChatID, e.Text); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	if c != nil {
		if pendingMatches(c.PendingClarification, e.Text, e.ReplyToMessageID) {
			if c.PendingClarification.Type == "image_unknown_needs_intent" {
				return true, nil
			}
		}
		if c.LatestLabelDraft != nil {
			if wantsLogLabelMeal(e.Text) || c.LatestLabelReply && (isMealAffirmation(e.Text) || labelCaptionGrams.MatchString(e.Text)) {
				return true, nil
			}
		}
	}
	return false, nil
}
func executeSpecializedText(ctx context.Context, o *Orchestrator, e AssistantEvent) (*telegram.IngestionResponse, error) {
	ctx = coachActionContext(ctx, e)
	if raw, ok := profileMarkdownText(e.Text); ok {
		return o.saveProfileMarkdown(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, raw)
	}
	if raw, ok := profileJSONText(e.Text); ok {
		return o.saveProfileJSON(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, raw)
	}
	if isDailyMealsQuestion(e.Text) {
		return o.answerDailyMeals(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID)
	}
	if correction, ok, err := o.mealCorrectionRequest(ctx, e.ChatID, e.Text); err != nil {
		return nil, err
	} else if ok {
		return o.correctMealCalories(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, correction)
	}
	if o.coachAgent == nil {
		return nil, fmt.Errorf("Coach context unavailable")
	}
	c, err := o.coachAgent.build(ctx, e.UserID, e.ChatID, e.MessageID, e.ReplyToMessageID)
	if err != nil {
		return nil, err
	}
	c.TurnText = e.Text
	if pendingMatches(c.PendingClarification, e.Text, e.ReplyToMessageID) {
		switch c.PendingClarification.Type {
		case "image_unknown_needs_intent":
			return o.continueImageIntent(ctx, e.UserID, e.ChatID, c.PendingClarification, e.Text)

		}
	}
	if res, handled, err := o.coachAgent.tryLabelMealFollowup(ctx, c, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.Text); handled || err != nil {
		return res, err
	}
	if isMealStatusQuestion(e.Text) {
		return o.coachAgent.answerMealStatus(ctx, c, e.UserID, e.ChatID, e.UpdateID, e.MessageID)
	}
	return nil, fmt.Errorf("text is not a specialized legacy flow")
}

func wantsPersonalIntrospection(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "кто я") || strings.Contains(lower, "что ты обо мне знаешь") || strings.Contains(lower, "что ты помнишь про меня") || strings.Contains(lower, "проект") || strings.Contains(lower, "в профиле") || strings.Contains(lower, "profile.md") || strings.Contains(lower, "profile.json") || strings.Contains(lower, "откуда ты") || strings.Contains(lower, "это откуда") || strings.Contains(lower, "из этого файла")
}
