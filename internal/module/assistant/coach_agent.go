package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jasmine/internal/module/prompts"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CoachAgent is the single text-turn path for coached session work. It reads
// PostgreSQL state before planning, executes typed actions through services,
// then renders only verified results.
type CoachAgent struct {
	owner    *Orchestrator
	live     *workout.LiveService
	planner  CoachPlanner
	registry *CoachToolRegistry
}

func NewCoachAgent(owner *Orchestrator, db *pgxpool.Pool) *CoachAgent {
	return &CoachAgent{owner: owner, live: workout.NewLiveService(db), planner: modelCoachPlanner{client: owner.aiClient, model: owner.model}, registry: NewCoachToolRegistry()}
}
func (a *CoachAgent) SetPromptStore(store prompts.Store) {
	if p, ok := a.planner.(modelCoachPlanner); ok {
		p.prompts = store
		a.planner = p
	}
}

func (a *CoachAgent) SetPlanner(p CoachPlanner) { a.planner = p }

// CoachImageObservation enters the same coach context as a text turn. Existing
// image analyzers return typed domain references and validated domain writes.
type CoachImageObservation struct {
	UserID                      string
	ChatID, UpdateID, MessageID int64
	ReplyToMessageID            *int64
	Caption                     string
	Image                       telegram.ImageInput
}

func (a *CoachAgent) ProcessImage(ctx context.Context, obs CoachImageObservation) (*telegram.IngestionResponse, error) {
	c, err := a.build(ctx, obs.UserID, obs.ChatID, obs.MessageID, obs.ReplyToMessageID)
	if err != nil {
		return nil, err
	}
	return a.owner.ingestImageObservation(ctx, obs.UserID, obs.ChatID, obs.UpdateID, obs.MessageID, obs.ReplyToMessageID, obs.Caption, obs.Image, c)
}

func (a *CoachAgent) build(ctx context.Context, userID string, chatID, currentMessageID int64, replyID *int64) (*CoachContext, error) {
	c := &CoachContext{UserID: userID, CurrentIndex: -1}
	if err := a.owner.db.QueryRow(ctx, `SELECT COALESCE(u.goal,''), COALESCE(NULLIF(NULLIF(u.timezone,'UTC'),''),NULLIF(tu.timezone,''),'UTC') FROM users u LEFT JOIN telegram_users tu ON tu.user_id=u.id WHERE u.id=$1`, userID).Scan(&c.Goal, &c.Timezone); err != nil {
		return nil, err
	}
	if a.owner.telegramRepo == nil {
		return c, nil
	}
	s, err := a.owner.telegramRepo.GetActiveWorkoutSession(ctx, userID)
	if err != nil && !errors.Is(err, telegram.ErrNotFound) {
		return nil, err
	}
	c.Session = s
	if s != nil {
		var p WorkoutProposalDraft
		if err := json.Unmarshal(s.ProposalData, &p); err != nil {
			return nil, err
		}
		c.Proposal = &p
		c.Performed, err = a.live.Get(ctx, userID, s.AppliedWorkoutID)
		if err != nil {
			return nil, err
		}
		for i := range p.Exercises {
			ex := &p.Exercises[i]
			n := performedCount(c.Performed, ex.ExerciseID)
			if n < len(ex.Sets) {
				c.Current = ex
				c.CurrentIndex = i
				c.CurrentSet = n + 1
				break
			}
		}
		if c.Current != nil && c.Performed != nil {
			for _, ex := range c.Performed.Exercises {
				if ex.ExerciseID == c.Current.ExerciseID && len(ex.Sets) > 0 {
					last := ex.Sets[len(ex.Sets)-1]
					c.LastSet = &last
				}
			}
		}
	}
	state, err := a.owner.telegramRepo.GetConversationState(ctx, userID)
	if err != nil && !errors.Is(err, telegram.ErrNotFound) {
		return nil, err
	}
	if state != nil {
		if state.FocusExerciseID != nil {
			c.FocusExerciseID = *state.FocusExerciseID
		}
		c.PendingClarification = state.PendingClarification
	}
	messages, err := a.owner.telegramRepo.ListMessages(ctx, chatID, 51)
	if err != nil {
		return nil, err
	}
	for _, m := range messages {
		if m.Direction == telegram.DirectionOutbound {
			c.LatestLabelReply = false
			for _, ref := range m.DomainReferences {
				if ref.Type == "image_workflow" && ref.Kind == string(ImageNutritionLabel) {
					c.LatestLabelReply = true
				}
			}
		}
		if m.MessageID != currentMessageID && (m.Direction == telegram.DirectionInbound || m.Direction == telegram.DirectionOutbound) {
			messageText := ""
			if m.Text != nil {
				messageText = strings.TrimSpace(*m.Text)
			} else if m.Direction == telegram.DirectionInbound {
				messageText = "[фото без подписи]"
			}
			if messageText != "" {
				c.RecentMessages = append(c.RecentMessages, CoachRecentMessage{MessageID: m.MessageID, Direction: m.Direction, Text: messageText})
			}
		}
		for _, ref := range m.DomainReferences {
			if ref.Type == "image_workflow" || ref.Type == "meal" || ref.Type == "body_measurement" || ref.Type == "product" {
				observation := ref
				if ref.Extra != nil {
					observation.Extra = make(map[string]any, len(ref.Extra))
					for key, value := range ref.Extra {
						if key != "draft" {
							observation.Extra[key] = value
						}
					}
				}
				c.Observations = append(c.Observations, observation)
			}
			if ref.Type == "image_workflow" {
				c.LatestLabelDraft = nil
				c.LatestLabelCaption = ""
				c.LatestLabelMessageID = 0
				if fileID, ok := ref.Extra["file_unique_id"].(string); ok {
					c.LatestImageFileID = fileID
				}
				if ref.Kind == string(ImageNutritionLabel) && ref.Extra["draft"] != nil {
					if raw, err := json.Marshal(ref.Extra["draft"]); err == nil {
						var draft NutritionLabelDraft
						if json.Unmarshal(raw, &draft) == nil && ValidateLabelDraft(&draft) == nil {
							c.LatestLabelDraft = &draft
							c.LatestLabelCaption, _ = ref.Extra["caption"].(string)
							if id, ok := ref.Extra["source_message_id"].(float64); ok {
								c.LatestLabelMessageID = int64(id)
							}
						}
					}
				}
			}
		}
	}
	if len(c.RecentMessages) > 50 {
		c.RecentMessages = c.RecentMessages[len(c.RecentMessages)-50:]
	}
	if replyID != nil {
		m, e := a.owner.telegramRepo.GetMessage(ctx, chatID, *replyID)
		if e == nil {
			for _, ref := range m.DomainReferences {
				if ref.Type == "exercise" {
					c.ReplyExerciseID = ref.ID
					break
				}
			}
		}
	}
	c.Memories, err = a.owner.telegramRepo.ListMemories(ctx, userID, nil, nil)
	if err != nil {
		return nil, err
	}
	coachMemories := c.Memories[:0]
	for _, memory := range c.Memories {
		if memory.Category != "personal_fact" {
			coachMemories = append(coachMemories, memory)
		}
	}
	c.Memories = coachMemories
	if len(c.Memories) > 30 {
		c.Memories = c.Memories[:30]
	}
	c.History, err = a.live.History(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(c.History) > 8 {
		c.History = c.History[:8]
	}
	policy := telegram.TurnContextPolicyFromContext(ctx)
	if !policy.UseConversationHistory || !policy.UseDomainHistory {
		c.RecentMessages = nil
	}
	if !policy.UseMemories {
		c.Memories = nil
	}
	if !policy.UseProfile {
		c.ProfileData = nil
		c.Goal = ""
	}
	if !policy.UseDomainHistory {
		c.History = nil
	}
	// Current session/performed state remains available for validation and writes.
	return c, nil
}
func performedCount(w *workout.Workout, id string) int {
	if w == nil {
		return 0
	}
	for _, e := range w.Exercises {
		if e.ExerciseID == id {
			return len(e.Sets)
		}
	}
	return 0
}
func (a *CoachAgent) Process(ctx context.Context, userID string, chatID, updateID, messageID int64, replyID *int64, text string) (*telegram.IngestionResponse, error) {
	if isLegacyBulkWorkoutReport(text) {
		return nil, nil
	}
	c, err := a.build(ctx, userID, chatID, messageID, replyID)
	if err != nil {
		return nil, fmt.Errorf("build coach context: %w", err)
	}
	c.TurnText = text
	if res, handled, err := a.tryLabelMealFollowup(ctx, c, userID, chatID, updateID, messageID, text); handled || err != nil {
		return res, err
	}
	if isMealStatusQuestion(text) {
		return a.answerMealStatus(ctx, c, userID, chatID, updateID, messageID)
	}
	if c.Session == nil && (workout.IsPlausibleWorkoutLog(text) || looksLikeHistoricalCorrection(text)) {
		return nil, nil
	}
	plan, err := a.planner.Plan(ctx, CoachTurn{Text: text, ReplyToMessageID: replyID}, c)
	if err != nil {
		return nil, err
	}
	if plan == nil || len(plan.Calls) == 0 {
		plan = ruleCoachPlan(CoachTurn{Text: text, ReplyToMessageID: replyID}, c)
		if plan == nil || len(plan.Calls) == 0 {
			if c.Session == nil {
				return nil, nil
			}
			plan = &CoachPlan{Calls: []CoachToolCall{{Name: "get_current_workout_state"}}}
		}
	}
	return a.executePlannedCalls(ctx, c, userID, chatID, updateID, messageID, text, plan.Calls)
}

// executePlannedCalls preserves Coach's existing atomic domain/audit/outbox path.
// Jasmine tools call it with one validated domain action; the old Coach planner
// may still pass a short plan for direct Orchestrator callers.
func (a *CoachAgent) executePlannedCalls(ctx context.Context, c *CoachContext, userID string, chatID, updateID, messageID int64, text string, calls []CoachToolCall) (*telegram.IngestionResponse, error) {
	plan := &CoachPlan{Calls: calls}
	if len(calls) == 0 {
		return nil, fmt.Errorf("empty coach action plan")
	}
	if len(plan.Calls) > 5 {
		return nil, fmt.Errorf("too many coach actions")
	}
	for _, call := range plan.Calls {
		if err := a.registry.Validate(call, c); err != nil {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "coach_action_invalid", Clarification: []string{"invalid_action_arguments"}, Errors: []string{err.Error()}}}, nil
		}
	}
	// Proposal creation already uses SessionService's atomic outbox path.
	if plan.Calls[len(plan.Calls)-1].Name == "propose_workout" {
		var transient, memory bool
		for _, call := range plan.Calls[:len(plan.Calls)-1] {
			switch call.Name {
			case "save_transient_state":
				transient = true
			case "save_memory":
				memory = true
			default:
				return nil, fmt.Errorf("unsupported action before proposal: %s", call.Name)
			}
		}
		return a.propose(ctx, c, userID, chatID, updateID, messageID, text, transient, memory)
	}
	tx, err := a.owner.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := a.owner.telegramRepo.LockUserTx(ctx, tx, userID); err != nil {
		return nil, err
	}
	var facts []string
	var refs []telegram.DomainReference
	for _, call := range plan.Calls {
		f, r, e := a.execute(ctx, tx, c, call, userID, chatID, updateID, messageID, text)
		if e != nil {
			return nil, e
		}
		if f != "" {
			facts = append(facts, f)
		}
		refs = append(refs, r...)
	}
	if len(facts) == 0 {
		return nil, nil
	}
	// The transaction stores verified facts. Delivery calls LLM_REPLY once, after commit.
	verifiedFacts := strings.Join(facts, "\n")
	actions := make([]string, 0, len(plan.Calls))
	state := map[string]any{"requested_calls": plan.Calls, "result_codes": facts}
	if c.Proposal != nil {
		state["proposal"] = c.Proposal
	}
	if c.Session != nil {
		state["session_status"] = c.Session.Status
		state["session_id"] = c.Session.ID
	}
	if c.Current != nil {
		state["current_exercise"] = c.Current
		state["current_set"] = c.CurrentSet
	}
	if c.LastSet != nil {
		state["last_set"] = c.LastSet
	}
	if c.Performed != nil {
		state["performed_workout"] = c.Performed
	}
	for i, call := range plan.Calls {
		if (call.Name == "save_memory" && facts[i] == "memory_saved") || (call.Name == "save_transient_state" && facts[i] == "transient_state_saved") {
			actions = append(actions, "coach."+call.Name)
		}
		if call.Name == "get_exercise_history" || call.Name == "get_progression" {
			state["workout_history"] = c.History
		}
		if len(refs) != 0 && call.Name != "get_active_session" && call.Name != "get_current_workout_state" && call.Name != "get_exercise_history" && call.Name != "get_progression" && call.Name != "fitness_question" && call.Name != "general_chat" {
			actions = append(actions, "coach."+call.Name)
		}
	}
	replyFacts := &telegram.ReplyContext{EventType: "coach_result", ActionsPerformed: actions, ToolResults: refs, CurrentState: state}
	if len(refs) == 0 && len(actions) == 0 && len(plan.Calls) > 0 {
		name := plan.Calls[0].Name
		if name != "get_active_session" && name != "get_current_workout_state" && name != "get_exercise_history" && name != "get_progression" && name != "fitness_question" && name != "general_chat" {
			replyFacts.Clarification = []string{"action_not_applied_or_needs_details"}
		}
	}
	res, err := a.owner.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, html.EscapeString(verifiedFacts), refs, replyFacts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
func (a *CoachAgent) execute(ctx context.Context, tx pgx.Tx, c *CoachContext, call CoachToolCall, userID string, chatID, updateID, messageID int64, text string) (string, []telegram.DomainReference, error) {
	var refs []telegram.DomainReference
	session := c.Session
	switch call.Name {
	case "get_active_session", "get_current_workout_state":
		if session == nil {
			return "no_active_workout", refs, nil
		}
		return "current_workout_state", []telegram.DomainReference{{Type: "workout_session", ID: session.ID}}, nil
	case "start_workout":
		if session == nil || session.Status != "proposed" {
			return "no_proposed_workout", refs, nil
		}
		if err := a.owner.sessionService.StartActiveSession(ctx, tx, userID, session); err != nil {
			return "", nil, err
		}
		session.Status = "in_progress"
		return "workout_started", []telegram.DomainReference{{Type: "workout_session", ID: session.ID}}, nil
	case "log_set":
		if session == nil || session.Status != "in_progress" {
			return "workout_not_started", nil, nil
		}
		ex, idx, ambiguous := resolveCoachExercise(c, call.Exercise, text)
		if ambiguous || ex == nil {
			return "exercise_or_set_ambiguous", nil, nil
		}
		next := performedCount(c.Performed, ex.ExerciseID) + 1
		if next > len(ex.Sets) {
			return "all_planned_sets_logged", nil, nil
		}
		if call.SetNumber != nil && *call.SetNumber != next {
			return "set_number_mismatch", nil, nil
		}
		if call.Reps == nil || *call.Reps < 1 || *call.Reps > 200 {
			return "reps_required", nil, nil
		}
		performed := workout.Set{SetNumber: next, Reps: call.Reps, Difficulty: call.Difficulty}
		prescribed := ex.Sets[next-1]
		performed.WeightKg = prescribed.WeightKg
		performed.AssistanceKg = prescribed.AssistanceKg
		performed.BaseWeightKg = prescribed.BaseWeightKg
		performed.AddedWeightKg = prescribed.AddedWeightKg
		if c.LastSet != nil && c.Current != nil && ex.ExerciseID == c.Current.ExerciseID {
			if c.LastSet.WeightKg != nil {
				performed.WeightKg = c.LastSet.WeightKg
			}
			if c.LastSet.AssistanceKg != nil {
				performed.AssistanceKg = c.LastSet.AssistanceKg
			}
		}
		explicit := explicitCoachWeight(text)
		if call.WeightKg != nil && explicit != nil && *call.WeightKg != *explicit {
			return "weight_conflict", nil, nil
		}
		call.WeightKg = explicit
		if call.WeightKg != nil && (prescribed.AssistanceKg != nil || prescribed.BaseWeightKg != nil || prescribed.AddedWeightKg != nil) {
			return "load_component_ambiguous", nil, nil
		}
		if call.AssistanceKg != nil && prescribed.AssistanceKg == nil {
			return "assistance_not_in_plan", nil, nil
		}
		if call.WeightKg != nil {
			performed.WeightKg = call.WeightKg
		}
		if call.AssistanceKg != nil {
			performed.AssistanceKg = call.AssistanceKg
		}
		id, err := a.live.LogSetTx(ctx, tx, userID, session.ID, ex.ExerciseID, idx, performed, updateID, messageID)
		if err != nil {
			return "", nil, err
		}
		session.AppliedWorkoutID = &id
		if c.Performed == nil {
			c.Performed = &workout.Workout{ID: id}
		}
		found := false
		for i := range c.Performed.Exercises {
			if c.Performed.Exercises[i].ExerciseID == ex.ExerciseID {
				c.Performed.Exercises[i].Sets = append(c.Performed.Exercises[i].Sets, performed)
				found = true
				break
			}
		}
		if !found {
			c.Performed.Exercises = append(c.Performed.Exercises, workout.WorkoutExercise{ExerciseID: ex.ExerciseID, ExerciseName: ex.ExerciseName, Sets: []workout.Set{performed}})
		}
		c.LastSet = &performed
		a.advance(c)
		return "set_logged", []telegram.DomainReference{{Type: "workout", ID: id}, {Type: "exercise", ID: ex.ExerciseID, Name: ex.ExerciseName}}, nil
	case "correct_performance":
		if session == nil || session.AppliedWorkoutID == nil || c.Performed == nil || len(c.Performed.Exercises) == 0 {
			return "set_to_correct_required", nil, nil
		}
		target := &c.Performed.Exercises[len(c.Performed.Exercises)-1]
		if call.Exercise != "" || c.ReplyExerciseID != "" || hasNamedExerciseHint(text) {
			ex, _, amb := resolveCoachExercise(c, call.Exercise, text)
			if amb || ex == nil {
				return "exercise_to_correct_required", nil, nil
			}
			var found *workout.WorkoutExercise
			for i := range c.Performed.Exercises {
				if c.Performed.Exercises[i].ExerciseID == ex.ExerciseID {
					found = &c.Performed.Exercises[i]
					break
				}
			}
			if found == nil {
				return "exercise_has_no_logged_sets", nil, nil
			}
			target = found
		}
		if len(target.Sets) == 0 {
			return "set_to_correct_required", nil, nil
		}
		last := target.Sets[len(target.Sets)-1]
		if call.SetNumber != nil && *call.SetNumber != last.SetNumber {
			return "only_last_set_correctable", nil, nil
		}
		replacement := workout.Set{Reps: call.Reps, WeightKg: call.WeightKg, AssistanceKg: call.AssistanceKg, Difficulty: call.Difficulty}
		if nums := coachCorrectionPattern.FindStringSubmatch(strings.ToLower(text)); len(nums) == 3 {
			oldValue, e1 := strconv.ParseFloat(strings.ReplaceAll(nums[1], ",", "."), 64)
			newValue, e2 := strconv.ParseFloat(strings.ReplaceAll(nums[2], ",", "."), 64)
			inferred := workout.Set{Difficulty: call.Difficulty}
			if e1 != nil || e2 != nil || newValue < 0 {
				return "correction_value_required", nil, nil
			}
			matches := 0
			if last.Reps != nil && float64(*last.Reps) == oldValue && newValue >= 1 && newValue <= 200 && newValue == float64(int(newValue)) {
				v := int(newValue)
				inferred.Reps = &v
				matches++
			}
			if last.WeightKg != nil && *last.WeightKg == oldValue && newValue <= 1000 {
				v := newValue
				inferred.WeightKg = &v
				matches++
			}
			if last.AssistanceKg != nil && *last.AssistanceKg == oldValue && newValue <= 300 {
				v := newValue
				inferred.AssistanceKg = &v
				matches++
			}
			if matches != 1 {
				return "correction_field_ambiguous", nil, nil
			}
			if call.Reps != nil && (inferred.Reps == nil || *call.Reps != *inferred.Reps) || call.WeightKg != nil && (inferred.WeightKg == nil || *call.WeightKg != *inferred.WeightKg) || call.AssistanceKg != nil && (inferred.AssistanceKg == nil || *call.AssistanceKg != *inferred.AssistanceKg) {
				return "correction_value_required", nil, nil
			}
			replacement = inferred
		} else if replacement.Reps == nil && replacement.WeightKg == nil && replacement.AssistanceKg == nil {
			return "correction_field_required", nil, nil
		}
		if err := a.live.CorrectLastSetTx(ctx, tx, userID, *session.AppliedWorkoutID, target.ExerciseID, replacement, updateID, messageID); err != nil {
			return "", nil, err
		}
		corrected := last
		if replacement.Reps != nil {
			corrected.Reps = replacement.Reps
		}
		if replacement.WeightKg != nil {
			corrected.WeightKg = replacement.WeightKg
		}
		if replacement.AssistanceKg != nil {
			corrected.AssistanceKg = replacement.AssistanceKg
		}
		if replacement.Difficulty != "" {
			corrected.Difficulty = replacement.Difficulty
		}
		target.Sets[len(target.Sets)-1] = corrected
		c.LastSet = &corrected
		return "set_corrected", []telegram.DomainReference{{Type: "workout", ID: *session.AppliedWorkoutID}, {Type: "exercise", ID: target.ExerciseID, Name: target.ExerciseName}}, nil
	case "add_exercise", "replace_exercise", "skip_exercise", "replan_active_workout":
		if session == nil || c.Proposal == nil {
			return "no_active_plan", nil, nil
		}
		return a.replan(ctx, tx, c, call, userID, updateID, text)
	case "abandon_workout":
		if session == nil {
			return "Нет активной тренировки.", nil, nil
		}
		if err := a.owner.sessionService.AbandonActiveSession(ctx, tx, userID, session); err != nil {
			return "", nil, err
		}
		session.Status = "abandoned"
		return "workout_abandoned_completed_sets_retained", []telegram.DomainReference{{Type: "workout_session", ID: session.ID}}, nil
	case "finish_workout":
		if session == nil || session.Status != "in_progress" {
			return "no_started_workout", nil, nil
		}
		if err := a.owner.sessionService.CompleteActiveSession(ctx, tx, userID, session, session.AppliedWorkoutID); err != nil {
			return "", nil, err
		}
		session.Status = "completed"
		return "workout_finished", []telegram.DomainReference{{Type: "workout_session", ID: session.ID}}, nil
	case "get_progression":
		return "progression_requested", nil, nil
	case "get_exercise_history":
		if len(c.History) == 0 {
			return "workout_history_empty", nil, nil
		}
		return "workout_history_available", nil, nil
	case "general_chat":
		c.ResponseIntent = "general_chat"
		return "general_chat", nil, nil
	case "fitness_question":
		if c.Current != nil {
			return "fitness_context_available", nil, nil
		}
		return "exercise_question_ambiguous", nil, nil
	case "save_memory":
		if a.owner.memoryExtractor == nil {
			return "memory_preference_required", nil, nil
		}
		memories, err := a.owner.memoryExtractor.ExtractCandidates(ctx, text, time.Local)
		if err != nil {
			return "", nil, err
		}
		if len(memories) == 0 {
			return "memory_value_required", nil, nil
		}
		if err := a.owner.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, memories); err != nil {
			return "", nil, err
		}
		return "memory_saved", nil, nil
	case "save_transient_state":
		mem := coachTransientMemory(text)
		if mem == nil {
			return "transient_state_required", nil, nil
		}
		if err := a.owner.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, []MemoryCandidate{*mem}); err != nil {
			return "", nil, err
		}
		return "transient_state_saved", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported coach tool %q", call.Name)
	}
}
func (a *CoachAgent) advance(c *CoachContext) {
	if c.Proposal == nil {
		return
	}
	c.Current = nil
	c.CurrentIndex = -1
	c.CurrentSet = 0
	c.LastSet = nil
	for i := range c.Proposal.Exercises {
		ex := &c.Proposal.Exercises[i]
		n := performedCount(c.Performed, ex.ExerciseID)
		if n < len(ex.Sets) {
			c.Current = ex
			c.CurrentIndex = i
			c.CurrentSet = n + 1
			if c.Performed != nil {
				for _, performed := range c.Performed.Exercises {
					if performed.ExerciseID == ex.ExerciseID && len(performed.Sets) > 0 {
						last := performed.Sets[len(performed.Sets)-1]
						c.LastSet = &last
					}
				}
			}
			break
		}
	}
}
func resolveCoachExercise(c *CoachContext, reference, text string) (*ProposedExerciseDraft, int, bool) {
	if c.Proposal == nil {
		return nil, -1, false
	}
	phrase := strings.ToLower(strings.TrimSpace(reference))
	if phrase == "" {
		phrase = strings.ToLower(text)
	}
	var matches []int
	for i, ex := range c.Proposal.Exercises {
		name := strings.ToLower(ex.ExerciseName)
		if strings.Contains(phrase, name) && name != "" {
			matches = append(matches, i)
		} else if strings.Contains(name, "leg curl") && strings.Contains(phrase, "лег курл") {
			matches = append(matches, i)
		} else if strings.Contains(name, "graviton") && strings.Contains(phrase, "гравитон") {
			matches = append(matches, i)
		} else if strings.Contains(name, "hip thrust") && strings.Contains(phrase, "хип траст") {
			matches = append(matches, i)
		} else if strings.Contains(name, "leg press") && strings.Contains(phrase, "лег пресс") {
			matches = append(matches, i)
		}
	}
	if len(matches) > 1 {
		return nil, -1, true
	}
	if len(matches) == 1 {
		i := matches[0]
		return &c.Proposal.Exercises[i], i, false
	}
	if strings.TrimSpace(reference) != "" {
		return nil, -1, true
	}
	for _, hint := range []string{"лег курл", "гравитон", "хип траст", "присед", "жим", "leg curl", "leg press", "squat", "hip thrust"} {
		if strings.Contains(phrase, hint) {
			return nil, -1, true
		}
	}
	if c.ReplyExerciseID != "" {
		for i := range c.Proposal.Exercises {
			if c.Proposal.Exercises[i].ExerciseID == c.ReplyExerciseID {
				return &c.Proposal.Exercises[i], i, false
			}
		}
	}
	if c.Current != nil {
		return c.Current, c.CurrentIndex, false
	}
	if c.FocusExerciseID != "" {
		for i := range c.Proposal.Exercises {
			if c.Proposal.Exercises[i].ExerciseID == c.FocusExerciseID {
				return &c.Proposal.Exercises[i], i, false
			}
		}
	}
	return nil, -1, false
}

var remainingMinutes = regexp.MustCompile(`(\d{1,3})\s*мин`)

func (a *CoachAgent) replan(ctx context.Context, tx pgx.Tx, c *CoachContext, call CoachToolCall, userID string, updateID int64, text string) (string, []telegram.DomainReference, error) {
	old, _ := json.Marshal(c.Proposal)
	p := *c.Proposal
	p.Exercises = append([]ProposedExerciseDraft(nil), c.Proposal.Exercises...)
	lower := strings.ToLower(text)
	switch call.Name {
	case "add_exercise":
		cands, err := a.owner.candidateSelector.SelectCandidates(ctx, nil, c.Memories, c.History, nil)
		if err != nil {
			return "", nil, err
		}
		used := map[string]bool{}
		for _, e := range p.Exercises {
			used[e.ExerciseID] = true
		}
		var choice *CandidateExercise
		for i := range cands {
			cand := &cands[i]
			if used[cand.ExerciseID] {
				continue
			}
			name := strings.ToLower(cand.Name)
			if strings.Contains(lower, name) || (strings.Contains(lower, "пресс") && cand.Category == "core") {
				choice = cand
				break
			}
		}
		if choice == nil {
			return "exercise_to_add_required", nil, nil
		}
		generated, err := a.owner.generator.generateDeterministicFallback(&WorkoutRequest{RawText: text}, nil, []CandidateExercise{*choice})
		if err != nil {
			return "", nil, err
		}
		p.Exercises = append(p.Exercises, generated.Exercises[0])
	case "replace_exercise", "skip_exercise":
		ex, idx, amb := resolveCoachExercise(c, call.Exercise, text)
		if amb || ex == nil {
			return "exercise_to_replace_required", nil, nil
		}
		done := performedCount(c.Performed, ex.ExerciseID)
		if done > 0 {
			p.Exercises[idx].Sets = append([]ProposedSetDraft(nil), ex.Sets[:done]...)
		} else {
			p.Exercises = append(p.Exercises[:idx], p.Exercises[idx+1:]...)
		}
		if call.Name == "replace_exercise" {
			req := &WorkoutRequest{RawText: text}
			cands, err := a.owner.candidateSelector.SelectCandidates(ctx, req, c.Memories, c.History, nil)
			if err != nil {
				return "", nil, err
			}
			used := map[string]bool{}
			for _, e := range p.Exercises {
				used[e.ExerciseID] = true
			}
			var choice *CandidateExercise
			for i := range cands {
				x := &cands[i]
				if !used[x.ExerciseID] && x.ExerciseID != ex.ExerciseID && x.Category == categoryFor(ex.ExerciseName) && (!strings.Contains(lower, "занят") || x.Equipment != "machine") && !strings.Contains(lower, strings.ToLower(x.Name)) {
					choice = x
					break
				}
			}
			if choice == nil {
				return "replacement_unavailable", nil, nil
			}
			generated, err := a.owner.generator.generateDeterministicFallback(req, nil, []CandidateExercise{*choice})
			if err != nil {
				return "", nil, err
			}
			replacement := generated.Exercises[0]
			if len(ex.Sets) > 0 && ex.Sets[0].Reps != nil {
				for j := range replacement.Sets {
					replacement.Sets[j].Reps = ex.Sets[0].Reps
				}
			}
			if done > 0 {
				p.Exercises = append(p.Exercises[:idx+1], append([]ProposedExerciseDraft{replacement}, p.Exercises[idx+1:]...)...)
			} else {
				p.Exercises = append(p.Exercises[:idx], append([]ProposedExerciseDraft{replacement}, p.Exercises[idx:]...)...)
			}
		}
	case "replan_active_workout":
		minutes := call.DurationMin
		if m := remainingMinutes.FindStringSubmatch(lower); len(m) > 1 {
			stated, _ := strconv.Atoi(m[1])
			if minutes != nil && *minutes != stated {
				return "remaining_minutes_required", nil, nil
			}
			minutes = &stated
		}
		if minutes == nil {
			if m := remainingMinutes.FindStringSubmatch(lower); len(m) > 1 {
				n, _ := strconv.Atoi(m[1])
				minutes = &n
			}
		}
		if minutes == nil && (strings.Contains(lower, "без них") || strings.Contains(lower, "ноги умерли")) {
			v := p.EstimatedDurationMin
			minutes = &v
		}
		if minutes == nil || *minutes < 5 || *minutes > 180 {
			return "remaining_minutes_required", nil, nil
		}
		p.EstimatedDurationMin = *minutes
		budget := *minutes / 4
		if budget < 1 {
			budget = 1
		}
		var kept []ProposedExerciseDraft
		for _, ex := range p.Exercises {
			done := performedCount(c.Performed, ex.ExerciseID)
			remaining := len(ex.Sets) - done
			if strings.Contains(lower, "без них") || strings.Contains(lower, "ноги умерли") {
				if categoryFor(ex.ExerciseName) == "lower" && done == 0 {
					continue
				}
				if categoryFor(ex.ExerciseName) == "lower" {
					remaining = 0
				}
			}
			take := remaining
			if take > budget {
				take = budget
			}
			if take < 0 {
				take = 0
			}
			if done+take > 0 {
				ex.Sets = append([]ProposedSetDraft(nil), ex.Sets[:done+take]...)
				kept = append(kept, ex)
			}
			budget -= take
		}
		p.Exercises = kept
	}
	if len(p.Exercises) == 0 {
		return "plan_has_no_remaining_exercises", nil, nil
	}
	allCatalog, err := a.owner.candidateSelector.SelectCandidates(ctx, nil, nil, nil, nil)
	if err != nil {
		return "", nil, err
	}
	if err := ValidateProposal(&p, allCatalog); err != nil {
		return "", nil, err
	}
	newData, err := json.Marshal(&p)
	if err != nil {
		return "", nil, err
	}
	if err := a.owner.telegramRepo.UpdateWorkoutSessionProposalTx(ctx, tx, c.Session.ID, userID, newData); err != nil {
		return "", nil, err
	}
	if err := a.owner.telegramRepo.SaveSessionRevisionTx(ctx, tx, c.Session.ID, &updateID, old, newData, text); err != nil {
		return "", nil, err
	}
	c.Proposal = &p
	a.advance(c)
	return "plan_revised_completed_sets_retained", []telegram.DomainReference{{Type: "workout_session", ID: c.Session.ID}}, nil
}
func categoryFor(name string) string {
	s := strings.ToLower(name)
	if strings.Contains(s, "leg") || strings.Contains(s, "hip") || strings.Contains(s, "squat") || strings.Contains(s, "ног") || strings.Contains(s, "ягод") {
		return "lower"
	}
	if strings.Contains(s, "press") || strings.Contains(s, "pull") || strings.Contains(s, "жим") {
		return "upper"
	}
	return "core"
}
func (a *CoachAgent) propose(ctx context.Context, c *CoachContext, userID string, chatID, updateID, messageID int64, text string, saveTransient, saveMemory bool) (*telegram.IngestionResponse, error) {
	if a.owner.generator == nil || a.owner.candidateSelector == nil || a.owner.sessionService == nil {
		return nil, nil
	}
	req := a.owner.requestExtractor.ExtractRequest(ctx, text)
	var transient *MemoryCandidate
	if saveTransient {
		transient = coachTransientMemory(text)
		if transient != nil {
			val, _ := json.Marshal(transient.Value)
			c.Memories = append(c.Memories, telegram.UserMemory{Category: transient.Category, Scope: transient.Scope, Key: transient.Key, Value: val, ValidUntil: transient.ValidUntil})
		}
	}
	var memoryCandidates []MemoryCandidate
	if saveMemory {
		if a.owner.memoryExtractor == nil {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "memory_preference_needed", Clarification: []string{"memory_preference"}}}, nil
		}
		var err error
		memoryCandidates, err = a.owner.memoryExtractor.ExtractCandidates(ctx, text, time.Local)
		if err != nil {
			return nil, err
		}
		if len(memoryCandidates) == 0 {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "memory_preference_needed", Clarification: []string{"memory_preference"}}}, nil
		}
		memoryCandidates = a.owner.memoryExtractor.CanonicalizeCandidates(ctx, memoryCandidates)
		for _, m := range memoryCandidates {
			if m.IsReversal {
				continue
			}
			val, _ := json.Marshal(m.Value)
			c.Memories = append(c.Memories, telegram.UserMemory{Category: m.Category, Scope: m.Scope, Key: m.Key, Value: val, ValidUntil: m.ValidUntil})
		}
	}
	candidates, err := a.owner.candidateSelector.SelectCandidates(ctx, req, c.Memories, c.History, nil)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "equipment_needed", Clarification: []string{"available_equipment"}}}, nil
	}
	conv := a.owner.contextBuilder.BuildContext(ctx, ActionWorkoutRequest, userID, nil, c.Memories, nil, c.History, nil, "")
	conv.Profile.Goal = c.Goal
	if a.owner.knowledge != nil && telegram.TurnContextPolicyFromContext(ctx).UseProfile && telegram.TurnContextPolicyFromContext(ctx).UseMemories {
		if facts, err := a.owner.knowledge.Relevant(ctx, userID, "тренировка"); err == nil && len(facts) > 0 {
			encoded, _ := json.Marshal(facts)
			conv.FormattedPromptContext = "Relevant current fitness facts: " + string(encoded) + "\n" + conv.FormattedPromptContext
		}
	}
	if c.Goal != "" {
		conv.FormattedPromptContext = "Goal: " + c.Goal + "\n" + conv.FormattedPromptContext
	}
	proposal, err := a.owner.generator.GenerateProposal(ctx, req, conv, candidates)
	if err != nil {
		return nil, err
	}
	tx, err := a.owner.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	res, err := a.owner.sessionService.CreateProposal(ctx, tx, userID, chatID, updateID, messageID, proposal)
	if err == nil && transient != nil {
		err = a.owner.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, []MemoryCandidate{*transient})
	}
	if err == nil && len(memoryCandidates) > 0 {
		err = a.owner.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, memoryCandidates)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

var coachCorrectionPattern = regexp.MustCompile(`не\s*(\d+(?:[.,]\d+)?).*?а\s*(\d+(?:[.,]\d+)?)`)
var bulkWorkoutNotation = regexp.MustCompile(`\d+\s*[xх×]\s*\d+`)

func isLegacyBulkWorkoutReport(text string) bool {
	lower := strings.ToLower(text)
	if !bulkWorkoutNotation.MatchString(lower) {
		return false
	}
	for _, hint := range []string{"лег курл", "лег курла", "хип траст", "жим", "присед", "гравитон", "curl", "press", "thrust"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}
func hasNamedExerciseHint(text string) bool {
	t := strings.ToLower(text)
	for _, hint := range []string{"лег курл", "гравитон", "хип траст", "лег пресс", "жим", "присед", "leg curl", "leg press", "squat", "hip thrust"} {
		if strings.Contains(t, hint) {
			return true
		}
	}
	return false
}
func coachTransientMemory(text string) *MemoryCandidate {
	t := strings.ToLower(text)
	if !strings.Contains(t, "убит") && !strings.Contains(t, "болит") && !strings.Contains(t, "устал") {
		return nil
	}
	until := time.Now().Add(24 * time.Hour)
	return &MemoryCandidate{Category: "transient_state", Scope: "transient", Key: "today_condition", Value: text, Confidence: 1, ValidUntil: &until, SourceText: text}
}
