package workout

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
)

// ErrNoCorrectionTarget means the input cannot be applied to prior workout data.
// It is expected for a first workout that an AI classifier mislabels as a correction.
var ErrNoCorrectionTarget = errors.New("no existing workout target for correction")

type TelegramMessageRetriever interface {
	GetMessage(ctx context.Context, chatID int64, messageID int64) (*telegram.TelegramMessage, error)
	GetConversationState(ctx context.Context, userID string) (*telegram.ConversationState, error)
}

type ReferentialResolver struct {
	workoutRepo  *Repository
	exerciseRes  *exercise.Resolver
	telegramRepo TelegramMessageRetriever
}

func NewReferentialResolver(workoutRepo *Repository, exerciseRes *exercise.Resolver, telegramRepo TelegramMessageRetriever) *ReferentialResolver {
	return &ReferentialResolver{
		workoutRepo:  workoutRepo,
		exerciseRes:  exerciseRes,
		telegramRepo: telegramRepo,
	}
}

// ResolveTarget determines the authoritative workout and exercise targeted by a conversational correction.
func (r *ReferentialResolver) ResolveTarget(
	ctx context.Context,
	userID string,
	chatID int64,
	replyToMessageID *int64,
	draft *CorrectionDraft,
) (*ResolvedTarget, error) {
	// Tier 1: Direct Telegram Reply Context
	if replyToMessageID != nil && r.telegramRepo != nil {
		target, err := r.resolveByReplyContext(ctx, chatID, *replyToMessageID, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, fmt.Errorf("resolve reply context: %w", err)
		}
		if err == nil && target != nil {
			return target, nil
		}
	}

	// Tier 2: Active Session Context
	var convState *telegram.ConversationState
	if r.telegramRepo != nil {
		var err error
		convState, err = r.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, fmt.Errorf("get conversation state: %w", err)
		}
	}

	if convState != nil && convState.ActiveSessionID != nil && *convState.ActiveSessionID != "" {
		// Session target check
	}

	// Tier 3: Current Conversational Focus (Unambiguous focus exercise)
	if convState != nil && convState.FocusExerciseID != nil && *convState.FocusExerciseID != "" && draft.ExerciseRawName == "" {
		target, err := r.resolveByFocusExercise(ctx, userID, *convState.FocusExerciseID, draft)
		if err != nil {
			return nil, fmt.Errorf("resolve focused exercise: %w", err)
		}
		if err == nil && target != nil {
			return target, nil
		}
	}

	// Tier 4: Explicit Exercise/Entity Tokens in Message
	if draft.ExerciseRawName != "" && r.exerciseRes != nil {
		res, err := r.exerciseRes.Resolve(ctx, draft.ExerciseRawName)
		if err != nil {
			return nil, fmt.Errorf("resolve exercise: %w", err)
		}
		if err == nil && res != nil && res.Exercise != nil {
			target, err := r.resolveByExplicitExercise(ctx, userID, res.Exercise.ID, res.Exercise.Name, draft)
			if err != nil {
				return nil, fmt.Errorf("resolve explicit exercise: %w", err)
			}
			if err == nil && target != nil {
				return target, nil
			}
		}
	}

	// Tier 5 & 6: Recent Relevant Workout Fallback & Ambiguity Detection
	return r.resolveByRecentWorkoutFallback(ctx, userID, draft)
}

func (r *ReferentialResolver) resolveByReplyContext(ctx context.Context, chatID int64, replyToMessageID int64, userID string) (*ResolvedTarget, error) {
	if r.telegramRepo == nil {
		return nil, nil
	}
	msg, err := r.telegramRepo.GetMessage(ctx, chatID, replyToMessageID)
	if err != nil || msg == nil {
		return nil, err
	}

	// Security: Isolate to matching user/chat
	if msg.UserID != userID && msg.Direction == telegram.DirectionInbound {
		return nil, fmt.Errorf("reply refers to message from different user")
	}

	var workoutID, exerciseID, exerciseName string
	for _, ref := range msg.DomainReferences {
		if ref.Type == "workout" {
			workoutID = ref.ID
		} else if ref.Type == "exercise" {
			exerciseID = ref.ID
			exerciseName = ref.Name
		}
	}

	if workoutID != "" {
		return &ResolvedTarget{
			WorkoutID:        workoutID,
			ExerciseID:       exerciseID,
			ExerciseName:     exerciseName,
			ResolutionSource: "reply_context",
		}, nil
	}

	return nil, nil
}

func (r *ReferentialResolver) resolveByFocusExercise(ctx context.Context, userID, focusExID string, draft *CorrectionDraft) (*ResolvedTarget, error) {
	if r.workoutRepo == nil {
		return &ResolvedTarget{
			WorkoutID:        "w-fallback",
			ExerciseID:       focusExID,
			ResolutionSource: "conversational_focus",
		}, nil
	}

	recent, err := r.workoutRepo.List(ctx, userID, "", "")
	if err != nil {
		return nil, fmt.Errorf("list recent workouts for focused exercise: %w", err)
	}
	if len(recent) == 0 {
		return nil, nil
	}

	latest := recent[0]
	for _, we := range latest.Exercises {
		if we.ExerciseID == focusExID {
			return &ResolvedTarget{
				WorkoutID:         latest.ID,
				WorkoutExerciseID: we.ID,
				ExerciseID:        we.ExerciseID,
				ExerciseName:      we.ExerciseName,
				ResolutionSource:  "conversational_focus",
			}, nil
		}
	}

	return nil, nil
}

func (r *ReferentialResolver) resolveByExplicitExercise(ctx context.Context, userID, exerciseID, exerciseName string, draft *CorrectionDraft) (*ResolvedTarget, error) {
	if r.workoutRepo == nil {
		return &ResolvedTarget{
			WorkoutID:        "w-fallback",
			ExerciseID:       exerciseID,
			ExerciseName:     exerciseName,
			ResolutionSource: "explicit_token",
		}, nil
	}

	recent, err := r.workoutRepo.List(ctx, userID, "", "")
	if err != nil {
		return nil, fmt.Errorf("list recent workouts for explicit exercise: %w", err)
	}
	if len(recent) == 0 {
		return nil, nil
	}

	for _, w := range recent {
		for _, we := range w.Exercises {
			if we.ExerciseID == exerciseID || NormalizeName(we.ExerciseName) == NormalizeName(exerciseName) {
				return &ResolvedTarget{
					WorkoutID:         w.ID,
					WorkoutExerciseID: we.ID,
					ExerciseID:        we.ExerciseID,
					ExerciseName:      we.ExerciseName,
					ResolutionSource:  "explicit_token",
				}, nil
			}
		}
	}

	return nil, nil
}

func (r *ReferentialResolver) resolveByRecentWorkoutFallback(ctx context.Context, userID string, draft *CorrectionDraft) (*ResolvedTarget, error) {
	if r.workoutRepo == nil {
		return &ResolvedTarget{
			WorkoutID:        "w-fallback",
			ExerciseID:       "ex-fallback",
			ResolutionSource: "recent_fallback",
		}, nil
	}

	recent, err := r.workoutRepo.List(ctx, userID, "", "")
	if err != nil {
		return nil, fmt.Errorf("list recent workouts: %w", err)
	}
	if len(recent) == 0 {
		return nil, ErrNoCorrectionTarget
	}

	latest := recent[0]
	if len(latest.Exercises) == 0 {
		return nil, ErrNoCorrectionTarget
	}

	if len(latest.Exercises) == 1 {
		we := latest.Exercises[0]
		return &ResolvedTarget{
			WorkoutID:         latest.ID,
			WorkoutExerciseID: we.ID,
			ExerciseID:        we.ExerciseID,
			ExerciseName:      we.ExerciseName,
			ResolutionSource:  "recent_fallback",
		}, nil
	}

	// Filter by old value if specified (e.g. user says "не 23, а 27")
	var matches []WorkoutExercise
	var oldVal float64
	hasOldVal := false
	if len(draft.Changes) > 0 && draft.Changes[0].OldValue != nil {
		oldVal = *draft.Changes[0].OldValue
		hasOldVal = true
	}

	if hasOldVal {
		for _, we := range latest.Exercises {
			for _, s := range we.Sets {
				if (s.WeightKg != nil && *s.WeightKg == oldVal) ||
					(s.BaseWeightKg != nil && *s.BaseWeightKg == oldVal) ||
					(s.AddedWeightKg != nil && *s.AddedWeightKg == oldVal) ||
					(s.AssistanceKg != nil && *s.AssistanceKg == oldVal) {
					matches = append(matches, we)
					break
				}
			}
		}
	}

	if len(matches) == 1 {
		we := matches[0]
		return &ResolvedTarget{
			WorkoutID:         latest.ID,
			WorkoutExerciseID: we.ID,
			ExerciseID:        we.ExerciseID,
			ExerciseName:      we.ExerciseName,
			ResolutionSource:  "recent_fallback",
		}, nil
	}

	// Ambiguity: multiple exercises matched or none matched specifically
	var candidateOptions []string
	for _, we := range latest.Exercises {
		candidateOptions = append(candidateOptions, we.ExerciseName)
	}

	var newStr string
	if len(draft.Changes) > 0 && draft.Changes[0].NewValue != nil {
		newStr = fmt.Sprintf("%.1f", *draft.Changes[0].NewValue)
	}

	prompt := fmt.Sprintf("Где исправить значение на %s кг: в %s?", newStr, strings.Join(candidateOptions, " или "))
	if hasOldVal {
		prompt = fmt.Sprintf("Где исправить %.1f → %s кг: в %s?", oldVal, newStr, strings.Join(candidateOptions, " или "))
	}

	return &ResolvedTarget{
		WorkoutID:        latest.ID,
		IsAmbiguous:      true,
		AmbiguousPrompt:  prompt,
		CandidateOptions: candidateOptions,
	}, nil
}

func NormalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
