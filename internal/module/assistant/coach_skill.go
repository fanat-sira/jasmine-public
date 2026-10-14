package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/identity"
)

// CoachSkill exposes domain actions without handing a whole text turn to the
// legacy orchestrator. Mutations reuse CoachAgent's atomic executor.
type CoachSkill struct{ agent *CoachAgent }

func NewCoachSkill(agent *CoachAgent) *CoachSkill { return &CoachSkill{agent: agent} }
func (s *CoachSkill) Name() string                { return "coach" }
func (s *CoachSkill) ContextProvider() JasmineContextProvider {
	return func(ctx context.Context, e AssistantEvent) (any, error) {
		c, err := s.agent.build(ctx, e.UserID, e.ChatID, e.MessageID, e.ReplyToMessageID)
		if err != nil {
			return nil, err
		}
		c.TurnText = e.Text
		return c, nil
	}
}

const coachNoArgs = `{"type":"object","additionalProperties":false}`
const coachActionArgs = `{"type":"object","properties":{"exercise":{"type":"string"},"reps":{"type":"integer"},"set_number":{"type":"integer"},"weight_kg":{"type":"number"},"assistance_kg":{"type":"number"},"difficulty":{"type":"string"},"duration_min":{"type":"integer"},"reason":{"type":"string"}},"additionalProperties":false}`
const coachSetArgs = `{"type":"object","properties":{"exercise":{"type":"string"},"reps":{"type":"integer"},"set_number":{"type":"integer"},"weight_kg":{"type":"number"},"assistance_kg":{"type":"number"},"difficulty":{"type":"string"}},"required":["reps"],"additionalProperties":false}`

func (s *CoachSkill) Tools() []JasmineTool {
	reads := []struct{ name, description string }{
		{"get_active_session", "Read whether a workout session exists"},
		{"get_current_workout_state", "Read current exercise, completed sets and next target"},
		{"get_exercise_history", "Read recent completed workouts"},
		{"get_progression", "Read training progression"},
		{"fitness_question", "Explain the current workout without changing it"},
	}
	tools := make([]JasmineTool, 0, 24)
	for _, item := range reads {
		name := item.name
		tools = append(tools, JasmineTool{Spec: JasmineToolSpec{Name: "coach." + name, Skill: "coach", Description: item.description, RiskLevel: RiskRead, ArgumentSchema: json.RawMessage(coachNoArgs)}, Handle: func(ctx context.Context, e AssistantEvent, state *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
			c, err := coachContextForEvent(state, e)
			if err != nil {
				return nil, err
			}
			call := CoachToolCall{Name: name}
			if err := s.agent.registry.Validate(call, c); err != nil {
				return nil, err
			}
			facts, refs, err := s.agent.execute(ctx, nil, c, call, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.Text)
			if err != nil {
				return nil, err
			}
			resultState := map[string]any{"references": refs}
			if c.Session != nil {
				resultState["session"] = c.Session
				resultState["proposal"] = c.Proposal
				resultState["current_exercise"] = c.Current
				resultState["current_set"] = c.CurrentSet
				resultState["performed_workout"] = c.Performed
			}
			if name == "get_exercise_history" || name == "get_progression" {
				resultState["workout_history"] = c.History
			}
			data, _ := json.Marshal(resultState)
			return &JasmineToolResult{Facts: facts, Data: data, OwnerUserID: e.UserID}, nil
		}})
	}
	writes := []struct{ name, description, schema string }{
		{"propose_workout", "Create a workout proposal for the user's current request", coachNoArgs},
		{"start_workout", "Start the currently proposed workout", coachNoArgs},
		{"log_set", "Log a performed set; use context for exercise and inherited load", coachSetArgs},
		{"correct_performance", "Correct the last performed set using user's wording", coachActionArgs},
		{"replace_exercise", "Replace an exercise while preserving completed work", coachActionArgs},
		{"skip_exercise", "Skip an exercise while preserving completed sets", coachActionArgs},
		{"add_exercise", "Add an exercise to the active plan", coachActionArgs},
		{"replan_active_workout", "Replan remaining work for time or condition", coachActionArgs},
		{"finish_workout", "Finish the active workout and retain the applied workout", coachNoArgs},
		{"abandon_workout", "Abandon active plan without deleting completed sets", coachNoArgs},
		{"save_memory", "Save a training preference from the user's text", coachNoArgs},
		{"save_transient_state", "Save today's training condition", coachNoArgs},
	}
	for _, item := range writes {
		name := item.name
		tools = append(tools, JasmineTool{Spec: JasmineToolSpec{Name: "coach." + name, Skill: "coach", Description: item.description, RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update", ArgumentSchema: json.RawMessage(item.schema)}, Handle: func(ctx context.Context, e AssistantEvent, state *JasmineContext, input json.RawMessage) (*JasmineToolResult, error) {
			c, err := coachContextForEvent(state, e)
			if err != nil {
				return nil, err
			}
			var call CoachToolCall
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&call); err != nil {
				return nil, err
			}
			call.Name = name // model cannot choose IDs or override the registered action
			revision := coachRevisionContext{Before: map[string]bool{}}
			if c.Proposal != nil {
				for _, ex := range c.Proposal.Exercises {
					revision.Before[ex.ExerciseID] = true
				}
				if name == "replace_exercise" || name == "skip_exercise" {
					if ex, _, amb := resolveCoachExercise(c, call.Exercise, e.Text); !amb && ex != nil {
						revision.OldExerciseID = ex.ExerciseID
						revision.OldExerciseName = ex.ExerciseName
					}
				}
			}
			calls := []CoachToolCall{call}
			if name == "propose_workout" {
				suggested := ruleCoachPlan(CoachTurn{Text: e.Text, ReplyToMessageID: e.ReplyToMessageID}, c)
				if suggested != nil {
					for _, candidate := range suggested.Calls {
						if candidate.Name == "save_transient_state" || candidate.Name == "save_memory" {
							calls = append([]CoachToolCall{candidate}, calls...)
						}
					}
				}
			}
			res, err := s.agent.executePlannedCalls(coachActionContext(ctx, e), c, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.Text, calls)
			if err != nil {
				return nil, err
			}
			return coachTerminalResult(ctx, s.agent, e, c, name, res, revision)
		}})
	}
	tools = append(tools, JasmineTool{Spec: JasmineToolSpec{Name: "coach.continue_workout_clarification", Skill: "coach", Description: "Resolve a pending exercise or workout-correction clarification", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update", ArgumentSchema: json.RawMessage(coachNoArgs)}, Handle: func(ctx context.Context, e AssistantEvent, state *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		c, err := coachContextForEvent(state, e)
		if err != nil {
			return nil, err
		}
		pending := c.PendingClarification
		if pending == nil {
			return nil, fmt.Errorf("no pending workout clarification")
		}
		ctx = coachActionContext(ctx, e)
		var res *telegram.IngestionResponse
		switch pending.Type {
		case "exercise_disambiguation":
			if s.agent.owner.ingestionPipe == nil {
				return nil, fmt.Errorf("workout ingestion unavailable")
			}
			res, err = s.agent.owner.ingestionPipe.ContinueClarification(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.Text, pending)
		case "correction_disambiguation":
			if s.agent.owner.correctionSvc == nil {
				return nil, fmt.Errorf("workout correction unavailable")
			}
			res, err = s.agent.owner.correctionSvc.ProcessCorrection(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.ReplyToMessageID, e.Text)
		default:
			return nil, fmt.Errorf("pending clarification %q is not a Coach action", pending.Type)
		}
		if err != nil {
			return nil, err
		}
		return coachTerminalResult(ctx, s.agent, e, c, "continue_workout_clarification", res)
	}})
	tools = append(tools, JasmineTool{Spec: JasmineToolSpec{Name: "coach.log_exercise", Skill: "coach", Description: "Log a complete workout report with several exercises", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update", ArgumentSchema: json.RawMessage(coachNoArgs)}, Handle: func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		if s.agent.owner.ingestionPipe == nil {
			return nil, fmt.Errorf("workout ingestion unavailable")
		}
		res, err := s.agent.owner.ingestionPipe.IngestWorkout(coachActionContext(ctx, e), e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.Text)
		if err != nil {
			return nil, err
		}
		return coachTerminalResult(ctx, s.agent, e, nil, "log_exercise", res)
	}})
	tools = append(tools, JasmineTool{Spec: JasmineToolSpec{Name: "coach.log_machine_metrics", Skill: "coach", Description: "Save metrics from a previously analyzed machine-display photo using its pending observation", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update", ArgumentSchema: json.RawMessage(`{"type":"object","properties":{"machine_name":{"type":"string"}},"required":["machine_name"],"additionalProperties":false}`)}, Handle: func(ctx context.Context, e AssistantEvent, state *JasmineContext, input json.RawMessage) (*JasmineToolResult, error) {
		c, err := coachContextForEvent(state, e)
		if err != nil {
			return nil, err
		}
		if c.PendingClarification == nil || c.PendingClarification.Type != "machine_display_needs_machine" {
			return nil, fmt.Errorf("no pending machine photo for this user")
		}
		var args struct {
			MachineName string `json:"machine_name"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, err
		}
		if strings.TrimSpace(args.MachineName) == "" {
			return nil, fmt.Errorf("machine name is empty")
		}
		var pending pendingMachineImage
		if err := json.Unmarshal(c.PendingClarification.Context, &pending); err != nil {
			return nil, err
		}
		res, err := s.agent.owner.logMachine(coachActionContext(ctx, e), e.UserID, e.ChatID, e.UpdateID, e.MessageID, args.MachineName, pending, []telegram.DomainReference{{Type: "image_workflow", Kind: "gym_machine_display"}})
		if err != nil {
			return nil, err
		}
		return coachTerminalResult(ctx, s.agent, e, c, "log_machine_metrics", res)
	}})
	return tools
}
func coachContextForEvent(state *JasmineContext, e AssistantEvent) (*CoachContext, error) {
	c, ok := state.SkillData["coach"].(*CoachContext)
	if !ok || c.UserID != e.UserID {
		return nil, fmt.Errorf("coach context belongs to another user")
	}
	return c, nil
}

type CoachActionFact struct {
	Action                  string                     `json:"action"`
	Applied                 bool                       `json:"applied"`
	SessionID               string                     `json:"session_id,omitempty"`
	WorkoutID               string                     `json:"workout_id,omitempty"`
	ExerciseID              string                     `json:"exercise_id,omitempty"`
	ExerciseName            string                     `json:"exercise_name,omitempty"`
	SetNumber               int                        `json:"set_number,omitempty"`
	Reps                    *int                       `json:"reps,omitempty"`
	WeightKg                *float64                   `json:"weight_kg,omitempty"`
	AssistanceKg            *float64                   `json:"assistance_kg,omitempty"`
	Difficulty              string                     `json:"difficulty,omitempty"`
	NextExercise            string                     `json:"next_exercise,omitempty"`
	Status                  string                     `json:"status,omitempty"`
	ReplyContext            *telegram.ReplyContext     `json:"reply_context,omitempty"`
	RevisionID              string                     `json:"revision_id,omitempty"`
	OldExerciseID           string                     `json:"old_exercise_id,omitempty"`
	OldExerciseName         string                     `json:"old_exercise_name,omitempty"`
	ReplacementExerciseID   string                     `json:"replacement_exercise_id,omitempty"`
	ReplacementExerciseName string                     `json:"replacement_exercise_name,omitempty"`
	CompletedExercises      int                        `json:"completed_exercises,omitempty"`
	CompletedSets           int                        `json:"completed_sets,omitempty"`
	TotalVolumeKg           float64                    `json:"total_volume_kg,omitempty"`
	References              []telegram.DomainReference `json:"references,omitempty"`
}

type coachRevisionContext struct {
	Before                         map[string]bool
	OldExerciseID, OldExerciseName string
}

func coachTerminalResult(ctx context.Context, agent *CoachAgent, e AssistantEvent, c *CoachContext, action string, res *telegram.IngestionResponse, revisions ...coachRevisionContext) (*JasmineToolResult, error) {
	if res == nil {
		return nil, fmt.Errorf("coach %s returned no response", action)
	}
	fact := CoachActionFact{Action: action, Applied: res.Success && !res.NeedsClarify && len(res.DomainReferences) > 0, ReplyContext: res.ReplyContext, References: res.DomainReferences}
	if c != nil {
		if c.Session != nil {
			fact.SessionID = c.Session.ID
			fact.Status = c.Session.Status
			if c.Session.AppliedWorkoutID != nil {
				fact.WorkoutID = *c.Session.AppliedWorkoutID
			}
		}
		if c.LastSet != nil {
			fact.SetNumber = c.LastSet.SetNumber
			fact.Reps = c.LastSet.Reps
			fact.WeightKg = c.LastSet.WeightKg
			fact.AssistanceKg = c.LastSet.AssistanceKg
			fact.Difficulty = c.LastSet.Difficulty
		}
		if c.Current != nil {
			fact.NextExercise = c.Current.ExerciseName
		}
	}
	for _, ref := range res.DomainReferences {
		if ref.Type == "exercise" {
			fact.ExerciseID = ref.ID
			fact.ExerciseName = ref.Name
		}
		if ref.Type == "workout" {
			fact.WorkoutID = ref.ID
		}
		if ref.Type == "workout_session" {
			fact.SessionID = ref.ID
		}
	}
	if fact.Applied && len(revisions) > 0 && c != nil && c.Proposal != nil {
		fact.OldExerciseID = revisions[0].OldExerciseID
		fact.OldExerciseName = revisions[0].OldExerciseName
		for _, ex := range c.Proposal.Exercises {
			if !revisions[0].Before[ex.ExerciseID] {
				fact.ReplacementExerciseID = ex.ExerciseID
				fact.ReplacementExerciseName = ex.ExerciseName
				break
			}
		}
	}
	if fact.Applied && action == "finish_workout" && c != nil && c.Performed != nil {
		fact.CompletedExercises = len(c.Performed.Exercises)
		for _, ex := range c.Performed.Exercises {
			fact.CompletedSets += len(ex.Sets)
		}
		if fact.WorkoutID != "" {
			_ = agent.owner.db.QueryRow(ctx, `SELECT total_volume FROM workouts WHERE id=$1`, fact.WorkoutID).Scan(&fact.TotalVolumeKg)
		}
	}
	if fact.Applied && fact.SessionID != "" && (action == "replace_exercise" || action == "skip_exercise" || action == "add_exercise" || action == "replan_active_workout") {
		_ = agent.owner.db.QueryRow(ctx, `SELECT id FROM workout_session_revisions WHERE session_id=$1 AND telegram_update_id=$2 ORDER BY created_at DESC LIMIT 1`, fact.SessionID, e.UpdateID).Scan(&fact.RevisionID)
	}
	data, err := json.Marshal(fact)
	if err != nil {
		return nil, err
	}
	return &JasmineToolResult{Facts: action, Data: data, OwnerUserID: e.UserID, Response: res, Done: true}, nil
}

func coachActionContext(ctx context.Context, e AssistantEvent) context.Context {
	return openrouter.WithSessionID(identity.WithUserID(ctx, e.UserID), conversationSessionID(e.ChatID))
}
