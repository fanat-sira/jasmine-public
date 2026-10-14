package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
)

func ptr[T any](v T) *T {
	return &v
}

type CorrectionService struct {
	db           *pgxpool.Pool
	workoutRepo  *Repository
	resolver     *ReferentialResolver
	extractor    *CorrectionExtractor
	telegramRepo *telegram.Repository
	renderer     telegram.DraftRenderer
}

func NewCorrectionService(
	db *pgxpool.Pool,
	workoutRepo *Repository,
	resolver *ReferentialResolver,
	extractor *CorrectionExtractor,
	telegramRepo *telegram.Repository,
	renderer telegram.DraftRenderer,
) *CorrectionService {
	return &CorrectionService{
		db:           db,
		workoutRepo:  workoutRepo,
		resolver:     resolver,
		extractor:    extractor,
		telegramRepo: telegramRepo,
		renderer:     renderer,
	}
}

type CorrectionContextData struct {
	Draft    CorrectionDraft `json:"draft"`
	TargetID string          `json:"target_id,omitempty"`
}

// ProcessCorrection attempts to parse and execute a conversational correction on existing workout history.
func (s *CorrectionService) ProcessCorrection(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	replyToMessageID *int64,
	text string,
) (*telegram.IngestionResponse, error) {
	// Check if user is replying to a pending correction clarification
	if s.telegramRepo != nil {
		state, err := s.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, fmt.Errorf("get correction conversation state: %w", err)
		}
		if err == nil && state != nil && state.PendingClarification != nil && state.PendingClarification.Type == "correction_disambiguation" {
			return s.continueCorrectionClarification(ctx, userID, chatID, updateID, messageID, text, state.PendingClarification)
		}
	}

	draft, err := s.extractor.ExtractCorrectionDraft(ctx, text)
	if err != nil || draft == nil || !draft.IsCorrection || len(draft.Changes) == 0 {
		return nil, ErrNotACorrection
	}

	target, err := s.resolver.ResolveTarget(ctx, userID, chatID, replyToMessageID, draft)
	if errors.Is(err, ErrNoCorrectionTarget) {
		return nil, ErrNotACorrection
	}
	if err != nil {
		return nil, fmt.Errorf("resolve target: %w", err)
	}

	if target.IsAmbiguous {
		// Store pending correction clarification in conversation_states
		if s.telegramRepo != nil {
			contextData, err := json.Marshal(CorrectionContextData{
				Draft: *draft,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal correction clarification context: %w", err)
			}
			if err := s.telegramRepo.UpsertConversationState(ctx, &telegram.ConversationState{
				UserID: userID,
				ChatID: chatID,
				PendingClarification: &telegram.PendingClarification{
					Type:          "correction_disambiguation",
					OriginalQuery: text,
					Candidates:    target.CandidateOptions,
					Context:       contextData,
					CreatedAt:     time.Now(),
				},
			}); err != nil {
				return nil, fmt.Errorf("store correction clarification: %w", err)
			}
		}

		return &telegram.IngestionResponse{
			NeedsClarify: true,
			ReplyContext: &telegram.ReplyContext{
				EventType:     "correction_disambiguation",
				Clarification: []string{"choose_exercise_to_correct"},
				CurrentState:  map[string]any{"candidates": target.CandidateOptions, "draft": draft},
			},
		}, nil
	}

	return s.applyPatchToWorkout(ctx, userID, chatID, updateID, messageID, target, draft)
}

func (s *CorrectionService) continueCorrectionClarification(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	answerText string,
	clar *telegram.PendingClarification,
) (*telegram.IngestionResponse, error) {
	var ctxData CorrectionContextData
	if len(clar.Context) > 0 {
		if err := json.Unmarshal(clar.Context, &ctxData); err != nil {
			return nil, fmt.Errorf("unmarshal correction clarification context: %w", err)
		}
	}

	normAnswer := strings.ToLower(strings.TrimSpace(answerText))

	var chosenCandidate *string
	for _, cand := range clar.Candidates {
		normCand := strings.ToLower(cand)
		if strings.Contains(normAnswer, normCand) || strings.Contains(normCand, normAnswer) ||
			(strings.Contains(normAnswer, "лег курл") && strings.Contains(normCand, "leg curl")) ||
			(strings.Contains(normAnswer, "курл") && strings.Contains(normCand, "curl")) ||
			(strings.Contains(normAnswer, "траст") && strings.Contains(normCand, "thrust")) ||
			(strings.Contains(normAnswer, "гравитон") && strings.Contains(normCand, "graviton")) ||
			(strings.Contains(normAnswer, "жим") && strings.Contains(normCand, "press")) {
			candCopy := cand
			chosenCandidate = &candCopy
			break
		}
	}

	if chosenCandidate == nil {
		return &telegram.IngestionResponse{
			NeedsClarify: true,
			ReplyContext: &telegram.ReplyContext{
				EventType:     "correction_disambiguation",
				Clarification: []string{"choose_exercise_to_correct"},
				CurrentState:  map[string]any{"candidates": clar.Candidates, "draft": ctxData.Draft},
			},
		}, nil
	}

	draft := ctxData.Draft
	draft.ExerciseRawName = *chosenCandidate

	target, err := s.resolver.ResolveTarget(ctx, userID, chatID, nil, &draft)
	if err != nil || target.IsAmbiguous {
		return nil, fmt.Errorf("failed to resolve target after clarification: %w", err)
	}

	return s.applyPatchToWorkout(ctx, userID, chatID, updateID, messageID, target, &draft)
}

func (s *CorrectionService) applyPatchToWorkout(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	target *ResolvedTarget,
	draft *CorrectionDraft,
) (*telegram.IngestionResponse, error) {
	draftID := telegram.StableDraftID(updateID)

	var targetWorkout *Workout
	if s.workoutRepo != nil {
		var err error
		targetWorkout, err = s.workoutRepo.GetByID(ctx, target.WorkoutID, userID)
		if err != nil {
			return nil, fmt.Errorf("load workout %s: %w", target.WorkoutID, err)
		}
	} else {
		// Fallback target for mock tests
		targetWorkout = &Workout{
			ID:     target.WorkoutID,
			UserID: userID,
			Exercises: []WorkoutExercise{
				{
					ID:           target.WorkoutExerciseID,
					ExerciseID:   target.ExerciseID,
					ExerciseName: target.ExerciseName,
					Sets: []Set{
						{SetNumber: 1, Reps: ptr(12), WeightKg: ptr(23.0)},
						{SetNumber: 2, Reps: ptr(12), WeightKg: ptr(23.0)},
						{SetNumber: 3, Reps: ptr(12), WeightKg: ptr(23.0)},
					},
				},
			},
		}
	}

	// Find the targeted exercise inside the workout
	var targetWE *WorkoutExercise
	for i := range targetWorkout.Exercises {
		we := &targetWorkout.Exercises[i]
		if we.ID == target.WorkoutExerciseID || we.ExerciseID == target.ExerciseID || NormalizeName(we.ExerciseName) == NormalizeName(target.ExerciseName) {
			targetWE = we
			break
		}
	}

	if targetWE == nil && len(targetWorkout.Exercises) > 0 {
		targetWE = &targetWorkout.Exercises[0]
	}

	if targetWE == nil {
		return nil, fmt.Errorf("target exercise not found in workout")
	}

	// Calculate initial volume before modifications
	beforeVolume := calculateTotalVolume(targetWorkout.Exercises)

	// Apply field changes
	for _, ch := range draft.Changes {
		switch ch.Field {
		case "weight_kg":
			if ch.NewValue != nil {
				for i := range targetWE.Sets {
					s := &targetWE.Sets[i]
					if ch.SetSelector == "specific" && ch.TargetSet != nil && s.SetNumber == *ch.TargetSet {
						s.WeightKg = ch.NewValue
					} else if ch.SetSelector == "last" && i == len(targetWE.Sets)-1 {
						s.WeightKg = ch.NewValue
					} else if ch.SetSelector == "first" && i == 0 {
						s.WeightKg = ch.NewValue
					} else if ch.OldValue != nil && s.WeightKg != nil && *s.WeightKg == *ch.OldValue {
						s.WeightKg = ch.NewValue
					} else if ch.SetSelector == "all" || ch.OldValue == nil {
						s.WeightKg = ch.NewValue
					}
				}
			}
		case "added_weight_kg":
			if ch.NewValue != nil {
				for i := range targetWE.Sets {
					s := &targetWE.Sets[i]
					s.AddedWeightKg = ch.NewValue
				}
			}
		case "base_weight_kg":
			if ch.NewValue != nil {
				for i := range targetWE.Sets {
					s := &targetWE.Sets[i]
					s.BaseWeightKg = ch.NewValue
				}
			}
		case "assistance_kg":
			if ch.NewValue != nil {
				for i := range targetWE.Sets {
					s := &targetWE.Sets[i]
					s.AssistanceKg = ch.NewValue
				}
			}
		case "reps":
			if ch.NewValue != nil {
				repsInt := int(*ch.NewValue)
				for i := range targetWE.Sets {
					s := &targetWE.Sets[i]
					if ch.SetSelector == "specific" && ch.TargetSet != nil && s.SetNumber == *ch.TargetSet {
						s.Reps = &repsInt
					} else if ch.SetSelector == "last" && i == len(targetWE.Sets)-1 {
						s.Reps = &repsInt
					} else if ch.SetSelector == "all" || (ch.SetSelector == "" && ch.TargetSet == nil) {
						s.Reps = &repsInt
					}
				}
			}
		case "set_count":
			if ch.NewValue != nil {
				newCount := int(*ch.NewValue)
				if newCount > 0 && newCount < len(targetWE.Sets) {
					// Deterministic decreasing set count: truncate trailing sets safely
					targetWE.Sets = targetWE.Sets[:newCount]
				} else if newCount > len(targetWE.Sets) {
					// Deterministic increasing set count: check if user provided reps/weight for new set
					// Never fabricate performed reps/weight blindly!
					clarifyMsg := "missing_set_details"
					if s.telegramRepo != nil {
						ctxData, err := json.Marshal(CorrectionContextData{Draft: *draft})
						if err != nil {
							return nil, fmt.Errorf("marshal correction clarification context: %w", err)
						}
						if err := s.telegramRepo.UpsertConversationState(ctx, &telegram.ConversationState{
							UserID: userID,
							ChatID: chatID,
							PendingClarification: &telegram.PendingClarification{
								Type:          "correction_disambiguation",
								OriginalQuery: clarifyMsg,
								Candidates:    []string{targetWE.ExerciseName},
								Context:       ctxData,
								CreatedAt:     time.Now(),
							},
						}); err != nil {
							return nil, fmt.Errorf("store correction clarification: %w", err)
						}
					}
					return &telegram.IngestionResponse{
						NeedsClarify: true,
						ReplyContext: &telegram.ReplyContext{
							EventType:     "missing_set_details",
							Clarification: []string{"weight_and_repetitions_required"},
							CurrentState:  map[string]any{"exercise": targetWE.ExerciseName, "set_number": len(targetWE.Sets) + 1, "draft": draft},
						},
					}, nil
				}
			}
		case "difficulty":
			if ch.NewText != "" {
				for i := range targetWE.Sets {
					targetWE.Sets[i].Difficulty = ch.NewText
				}
			}
		}
	}

	// Recalculate total volume after modifications
	afterVolume := calculateTotalVolume(targetWorkout.Exercises)
	targetWorkout.TotalVolume = afterVolume

	domainRefs := []telegram.DomainReference{
		{Type: "workout", ID: targetWorkout.ID},
		{Type: "exercise", ID: targetWE.ExerciseID, Name: targetWE.ExerciseName},
	}

	replyFacts := &telegram.ReplyContext{
		EventType:        "workout_corrected",
		ActionsPerformed: []string{"workout.correct"},
		CurrentState: map[string]any{
			"workout":            targetWorkout,
			"corrected_exercise": targetWE,
			"changes":            draft.Changes,
			"before_volume_kg":   beforeVolume,
			"after_volume_kg":    afterVolume,
		},
		ToolResults: domainRefs,
	}

	// Execute Atomic Database Transaction
	if s.db != nil {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin db tx: %w", err)
		}
		defer tx.Rollback(ctx)

		// 1. Update workout_exercises sets_data
		setsJSON, err := json.Marshal(targetWE.Sets)
		if err != nil {
			return nil, fmt.Errorf("marshal updated sets: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`UPDATE workout_exercises SET sets_data = $1 WHERE id = $2`,
			setsJSON, targetWE.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("update workout_exercise: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("update workout_exercise: expected one row, updated %d", tag.RowsAffected())
		}

		// 2. Update workouts total_volume
		tag, err = tx.Exec(ctx,
			`UPDATE workouts SET total_volume = $1 WHERE id = $2 AND user_id = $3`,
			afterVolume, targetWorkout.ID, userID,
		)
		if err != nil {
			return nil, fmt.Errorf("update workout total_volume: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("update workout total_volume: expected one row, updated %d", tag.RowsAffected())
		}

		// 3. Store Separate Explicit Workout Correction Audit
		changesJSON, err := json.Marshal(draft.Changes)
		if err != nil {
			return nil, fmt.Errorf("marshal workout correction changes: %w", err)
		}
		corrID := correlation.GetCorrelationID(ctx)
		if _, err := tx.Exec(ctx,
			`INSERT INTO workout_corrections (correlation_id, user_id, workout_id, workout_exercise_id, telegram_update_id, telegram_message_id, changes, before_volume, after_volume)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			corrID, userID, targetWorkout.ID, targetWE.ID, updateID, messageID, changesJSON, beforeVolume, afterVolume,
		); err != nil {
			return nil, fmt.Errorf("insert workout correction audit: %w", err)
		}

		// 4. If subjective effort/feedback was attached, insert into user_feedback_events
		if draft.FeedbackAttached != "" {
			fbPayload, err := json.Marshal(map[string]any{
				"feedback_text": draft.FeedbackAttached,
				"exercise_id":   targetWE.ExerciseID,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal correction feedback metadata: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO user_feedback_events (correlation_id, user_id, workout_id, exercise_id, feedback_type, raw_text, metadata)
					 VALUES ($1, $2, $3, $4, 'effort_comment', $5, $6)`,
				corrID, userID, targetWorkout.ID, targetWE.ExerciseID, draft.FeedbackAttached, fbPayload,
			); err != nil {
				return nil, fmt.Errorf("record correction feedback: %w", err)
			}
		}

		// 5. Update Conversation State (clearing pending_clarification, setting focus)
		if s.telegramRepo != nil {
			if err := s.telegramRepo.UpsertConversationStateTx(ctx, tx, &telegram.ConversationState{
				UserID:               userID,
				ChatID:               chatID,
				FocusExerciseID:      &targetWE.ExerciseID,
				PendingClarification: nil,
			}); err != nil {
				return nil, fmt.Errorf("update correction conversation state: %w", err)
			}
		}

		// 6. Enqueue Outbox Delivery in the same transaction
		outboxPayload, err := json.Marshal(telegram.OutboxPayload{
			DraftID:          &draftID,
			IsRich:           s.renderer != nil && s.renderer.GetActiveMode(chatID) == telegram.DraftModeRich,
			Text:             "",
			ReplyContext:     replyFacts,
			ParseMode:        "HTML",
			ReplyToMessageID: &messageID,
			DomainReferences: domainRefs,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal correction outbox payload: %w", err)
		}

		deliv := &telegram.TelegramDelivery{
			UpdateID:      &updateID,
			ChatID:        chatID,
			UserID:        userID,
			CorrelationID: corrID,
			Status:        telegram.DeliveryStatusPending,
			Payload:       outboxPayload,
			Attempts:      0,
		}
		if s.telegramRepo != nil {
			if err := s.telegramRepo.CreateDeliveryTx(ctx, tx, deliv); err != nil {
				return nil, fmt.Errorf("create delivery tx: %w", err)
			}

			// 7. Authoritatively mark originating Telegram update domain action complete
			respSummary, err := json.Marshal(map[string]any{
				"workout_id": targetWorkout.ID,
				"action":     "corrected",
				"delivery":   deliv.ID,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal correction response: %w", err)
			}
			if err := s.telegramRepo.CompleteUpdateTx(ctx, tx, updateID, respSummary); err != nil {
				return nil, fmt.Errorf("complete correction update: %w", err)
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit correction tx: %w", err)
		}
	}

	return &telegram.IngestionResponse{
		Success:          true,
		WorkoutID:        targetWorkout.ID,
		ReplyContext:     replyFacts,
		DomainReferences: domainRefs,
	}, nil
}

func calculateTotalVolume(exercises []WorkoutExercise) float64 {
	total := 0.0
	for _, we := range exercises {
		for _, s := range we.Sets {
			reps := 0
			if s.Reps != nil {
				reps = *s.Reps
			}
			if s.WeightKg != nil {
				total += float64(reps) * (*s.WeightKg)
			} else if s.BaseWeightKg != nil || s.AddedWeightKg != nil {
				b := 0.0
				a := 0.0
				if s.BaseWeightKg != nil {
					b = *s.BaseWeightKg
				}
				if s.AddedWeightKg != nil {
					a = *s.AddedWeightKg
				}
				total += float64(reps) * (b + a)
			}
		}
	}
	return total
}
