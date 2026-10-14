package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
)

type IngestionPipeline struct {
	db            *pgxpool.Pool
	workoutRepo   *Repository
	exerciseRes   *exercise.Resolver
	extractor     *Extractor
	correctionSvc *CorrectionService
	telegramRepo  *telegram.Repository
	renderer      telegram.DraftRenderer
}

func NewIngestionPipeline(
	db *pgxpool.Pool,
	workoutRepo *Repository,
	exerciseRes *exercise.Resolver,
	extractor *Extractor,
	telegramRepo *telegram.Repository,
	renderer telegram.DraftRenderer,
) *IngestionPipeline {
	return &IngestionPipeline{
		db:           db,
		workoutRepo:  workoutRepo,
		exerciseRes:  exerciseRes,
		extractor:    extractor,
		telegramRepo: telegramRepo,
		renderer:     renderer,
	}
}

func (p *IngestionPipeline) SetCorrectionService(svc *CorrectionService) {
	p.correctionSvc = svc
}

type ResolvedItem struct {
	DraftEx  WorkoutDraftExercise
	CanonEx  *exercise.Exercise
	SetsData []Set
}

type ClarificationContextData struct {
	Draft         WorkoutDraft `json:"draft"`
	ExerciseIndex int          `json:"exercise_index"`
	RawName       string       `json:"raw_name"`
}

// IngestMessage handles inbound Telegram text with intent gating, conversational corrections, and workout ingestion.
func (p *IngestionPipeline) IngestMessage(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	replyToMessageID *int64,
	rawText string,
) (*telegram.IngestionResponse, error) {
	// 1. Check if user has an active pending clarification
	var pendingClar *telegram.PendingClarification
	if p.telegramRepo != nil {
		state, err := p.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, fmt.Errorf("get workout conversation state: %w", err)
		}
		if state != nil && state.PendingClarification != nil {
			pendingClar = state.PendingClarification
		}
	}

	if pendingClar != nil {
		if pendingClar.Type == "exercise_disambiguation" {
			return p.ContinueClarification(ctx, userID, chatID, updateID, messageID, rawText, pendingClar)
		} else if pendingClar.Type == "correction_disambiguation" && p.correctionSvc != nil {
			return p.correctionSvc.continueCorrectionClarification(ctx, userID, chatID, updateID, messageID, rawText, pendingClar)
		}
	}

	// 2. Check if message is a conversational correction (e.g. "не 23, а 27", "в гравитоне ассист 36", reply-based)
	if p.correctionSvc != nil {
		corrRes, err := p.correctionSvc.ProcessCorrection(ctx, userID, chatID, updateID, messageID, replyToMessageID, rawText)
		if err != nil && !errors.Is(err, ErrNotACorrection) {
			return nil, fmt.Errorf("process workout correction: %w", err)
		}
		if corrRes != nil {
			return corrRes, nil
		}
	}

	// 3. Intent Gate: Only process messages that are plausibly workout reports
	if !IsPlausibleWorkoutLog(rawText) {
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "general_chat"}}, nil
	}

	return p.IngestWorkout(ctx, userID, chatID, updateID, messageID, rawText)
}

// IsPlausibleWorkoutLog checks if natural text contains workout notation rather than general chat/questions.
func IsPlausibleWorkoutLog(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}

	// Conversational / question exclusions
	if strings.HasPrefix(t, "что сегодня") || strings.HasPrefix(t, "как у меня") ||
		strings.HasPrefix(t, "привет") || strings.HasPrefix(t, "здравствуй") ||
		strings.HasPrefix(t, "что я делал") || strings.HasPrefix(t, "как правильно") ||
		strings.HasPrefix(t, "составь") || strings.HasPrefix(t, "сделай мне тренировку") ||
		strings.HasPrefix(t, "план") || strings.HasPrefix(t, "помощь") || strings.HasPrefix(t, "/help") ||
		strings.HasPrefix(t, "подскажи") || strings.HasPrefix(t, "сколько") {
		return false
	}

	// Workout signals (reps/sets, weight, machine assistance notations)
	hasSetsReps := regexp.MustCompile(`\d+\s*[xх*×/]\s*\d+`).MatchString(t) ||
		regexp.MustCompile(`\d+\s*(по|подход|повт|раз)`).MatchString(t)
	hasWeight := regexp.MustCompile(`\d+(\.\d+)?\s*(кг|kg)`).MatchString(t) ||
		strings.Contains(t, "ассист") || strings.Contains(t, "тренажер") || strings.Contains(t, "тренажёр") ||
		strings.Contains(t, "отказ")

	return hasSetsReps || hasWeight
}

// IngestWorkout parses and commits a fresh workout report.
func (p *IngestionPipeline) IngestWorkout(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	rawText string,
) (*telegram.IngestionResponse, error) {
	return p.IngestWorkoutTx(ctx, nil, userID, chatID, updateID, messageID, rawText)
}

// IngestWorkoutTx parses and commits a workout report inside an existing transaction (or opens one if tx is nil).
func (p *IngestionPipeline) IngestWorkoutTx(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	rawText string,
) (*telegram.IngestionResponse, error) {
	// Stage 1: Status Draft

	// Stage 2: LLM Extraction
	draft, err := p.extractor.ExtractWorkoutDraft(ctx, rawText)
	if err != nil {
		return nil, fmt.Errorf("extraction error: %w", err)
	}

	// Stage 3: Exercise Resolution

	var resolved []ResolvedItem
	var domainRefs []telegram.DomainReference

	for i, exDraft := range draft.Exercises {
		res, err := p.exerciseRes.Resolve(ctx, exDraft.RawName)
		if err != nil {
			return nil, fmt.Errorf("resolve exercise %q: %w", exDraft.RawName, err)
		}

		if res.Status == exercise.ResolutionAmbiguous || res.Status == exercise.ResolutionNotFound || res.Exercise == nil {
			// Stage 4: Ambiguity -> Preserve draft and ask clarification
			if p.telegramRepo != nil {
				candidateNames := make([]string, 0, len(res.Candidates))
				for _, c := range res.Candidates {
					candidateNames = append(candidateNames, c.Name)
				}
				contextData, err := json.Marshal(ClarificationContextData{
					Draft:         *draft,
					ExerciseIndex: i,
					RawName:       exDraft.RawName,
				})
				if err != nil {
					return nil, fmt.Errorf("marshal exercise clarification context: %w", err)
				}

				if err := p.telegramRepo.UpsertConversationState(ctx, &telegram.ConversationState{
					UserID: userID,
					ChatID: chatID,
					PendingClarification: &telegram.PendingClarification{
						Type:          "exercise_disambiguation",
						OriginalQuery: exDraft.RawName,
						Candidates:    candidateNames,
						Context:       contextData,
						CreatedAt:     time.Now(),
					},
				}); err != nil {
					return nil, fmt.Errorf("store exercise clarification: %w", err)
				}
			}

			return &telegram.IngestionResponse{
				NeedsClarify: true,
				ReplyContext: &telegram.ReplyContext{
					EventType:     "exercise_disambiguation",
					Clarification: []string{"choose_exercise"},
					CurrentState:  map[string]any{"raw_name": exDraft.RawName, "candidates": res.Candidates, "draft": draft},
				},
			}, nil
		}

		var sets []Set
		for _, s := range exDraft.Sets {
			sets = append(sets, Set{
				SetNumber:     s.SetNumber,
				Reps:          s.Reps,
				WeightKg:      s.WeightKg,
				BaseWeightKg:  s.BaseWeightKg,
				AddedWeightKg: s.AddedWeightKg,
				AssistanceKg:  s.AssistanceKg,
				RPE:           s.RPE,
				Difficulty:    exDraft.Difficulty,
				TimeMin:       s.TimeMin,
				DistanceKm:    s.DistanceKm,
				Resistance:    s.Resistance,
				Notes:         s.Notes,
			})
		}

		resolved = append(resolved, ResolvedItem{
			DraftEx:  exDraft,
			CanonEx:  res.Exercise,
			SetsData: sets,
		})

		domainRefs = append(domainRefs, telegram.DomainReference{
			Type: "exercise",
			ID:   res.Exercise.ID,
			Name: res.Exercise.Name,
		})
	}

	return p.commitResolvedWorkoutTx(ctx, tx, userID, chatID, updateID, messageID, draft, resolved, domainRefs)
}

// ContinueClarification resolves the pending ambiguous exercise using the user's reply.
func (p *IngestionPipeline) ContinueClarification(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	answerText string,
	clar *telegram.PendingClarification,
) (*telegram.IngestionResponse, error) {
	return p.ContinueClarificationTx(ctx, nil, userID, chatID, updateID, messageID, answerText, clar)
}

// ContinueClarificationTx resolves the pending ambiguous exercise inside an existing transaction.
func (p *IngestionPipeline) ContinueClarificationTx(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	answerText string,
	clar *telegram.PendingClarification,
) (*telegram.IngestionResponse, error) {
	var ctxData ClarificationContextData
	if len(clar.Context) > 0 {
		if err := json.Unmarshal(clar.Context, &ctxData); err != nil {
			return nil, fmt.Errorf("unmarshal exercise clarification context: %w", err)
		}
	}

	normAnswer := exercise.NormalizeQuery(answerText)

	// Disambiguate answer against candidates
	var chosenCandidate *string
	for _, cand := range clar.Candidates {
		normCand := exercise.NormalizeQuery(cand)
		if strings.Contains(normAnswer, normCand) || strings.Contains(normCand, normAnswer) ||
			(strings.Contains(normAnswer, "сидя") && strings.Contains(normCand, "сидя")) ||
			(strings.Contains(normAnswer, "лежа") && strings.Contains(normCand, "лежа")) ||
			(strings.Contains(normAnswer, "наклон") && strings.Contains(normCand, "наклон")) {
			candCopy := cand
			chosenCandidate = &candCopy
			break
		}
	}

	if chosenCandidate == nil {
		// Still ambiguous: ask again concisely without discarding the preserved draft
		return &telegram.IngestionResponse{
			NeedsClarify: true,
			ReplyContext: &telegram.ReplyContext{
				EventType:     "exercise_disambiguation",
				Clarification: []string{"choose_exercise"},
				CurrentState:  map[string]any{"raw_name": clar.OriginalQuery, "candidates": clar.Candidates, "draft": ctxData.Draft},
			},
		}, nil
	}

	// Resolve the chosen candidate
	res, err := p.exerciseRes.Resolve(ctx, *chosenCandidate)
	if err != nil || res.Exercise == nil {
		return nil, fmt.Errorf("resolve clarified exercise: %w", err)
	}

	draft := ctxData.Draft
	idx := ctxData.ExerciseIndex
	if idx >= 0 && idx < len(draft.Exercises) {
		draft.Exercises[idx].RawName = res.Exercise.Name
	}

	// Re-resolve all exercises in preserved draft
	var resolved []ResolvedItem
	var domainRefs []telegram.DomainReference

	for _, exDraft := range draft.Exercises {
		r, err := p.exerciseRes.Resolve(ctx, exDraft.RawName)
		if err != nil || r.Exercise == nil {
			return nil, fmt.Errorf("resolve exercise: %w", err)
		}

		var sets []Set
		for _, s := range exDraft.Sets {
			sets = append(sets, Set{
				SetNumber:     s.SetNumber,
				Reps:          s.Reps,
				WeightKg:      s.WeightKg,
				BaseWeightKg:  s.BaseWeightKg,
				AddedWeightKg: s.AddedWeightKg,
				AssistanceKg:  s.AssistanceKg,
				RPE:           s.RPE,
				Difficulty:    exDraft.Difficulty,
				TimeMin:       s.TimeMin,
				DistanceKm:    s.DistanceKm,
				Resistance:    s.Resistance,
				Notes:         s.Notes,
			})
		}

		resolved = append(resolved, ResolvedItem{
			DraftEx:  exDraft,
			CanonEx:  r.Exercise,
			SetsData: sets,
		})

		domainRefs = append(domainRefs, telegram.DomainReference{
			Type: "exercise",
			ID:   r.Exercise.ID,
			Name: r.Exercise.Name,
		})
	}

	return p.commitResolvedWorkoutTx(ctx, tx, userID, chatID, updateID, messageID, &draft, resolved, domainRefs)
}

func (p *IngestionPipeline) commitResolvedWorkoutTx(
	ctx context.Context,
	callerTx pgx.Tx,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	draft *WorkoutDraft,
	resolved []ResolvedItem,
	domainRefs []telegram.DomainReference,
) (*telegram.IngestionResponse, error) {
	draftID := telegram.StableDraftID(updateID)
	totalVolume := 0.0
	var workoutInputs []WorkoutExerciseInput

	for i, r := range resolved {
		for _, s := range r.SetsData {
			reps := 0
			if s.Reps != nil {
				reps = *s.Reps
			}
			if s.WeightKg != nil {
				totalVolume += float64(reps) * (*s.WeightKg)
			} else if s.BaseWeightKg != nil || s.AddedWeightKg != nil {
				base := 0.0
				added := 0.0
				if s.BaseWeightKg != nil {
					base = *s.BaseWeightKg
				}
				if s.AddedWeightKg != nil {
					added = *s.AddedWeightKg
				}
				totalVolume += float64(reps) * (base + added)
			}
		}

		workoutInputs = append(workoutInputs, WorkoutExerciseInput{
			ExerciseID:   r.CanonEx.ID,
			ExerciseName: r.CanonEx.Name,
			Sets:         r.SetsData,
			Notes:        r.DraftEx.Notes,
			OrderIndex:   i,
		})
	}

	workoutModel := &Workout{
		UserID:           userID,
		StartedAt:        time.Now(),
		DurationMin:      45,
		WorkoutType:      draft.WorkoutType,
		TotalVolume:      totalVolume,
		Notes:            &draft.Notes,
		Source:           "telegram",
		TelegramUpdateID: &updateID,
	}

	replyFacts := &telegram.ReplyContext{
		EventType:        "workout_logged",
		ActionsPerformed: []string{"workout.log"},
		CurrentState: map[string]any{
			"workout_type":    workoutModel.WorkoutType,
			"added_exercises": workoutInputs,
			"added_volume_kg": totalVolume,
		},
		ToolResults: domainRefs,
	}

	var tx pgx.Tx = callerTx
	var err error
	ownsTx := false

	if tx == nil && p.db != nil {
		tx, err = p.db.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin db tx: %w", err)
		}
		ownsTx = true
		defer func() {
			if ownsTx && tx != nil {
				_ = tx.Rollback(ctx)
			}
		}()
	}

	if tx != nil {
		var activeSession *telegram.WorkoutSession
		if p.telegramRepo != nil {
			activeSession, err = p.telegramRepo.GetActiveWorkoutSessionForUpdate(ctx, tx, userID)
			if err != nil && !errors.Is(err, telegram.ErrNotFound) {
				return nil, fmt.Errorf("lock active workout session: %w", err)
			}
		}

		if activeSession != nil && activeSession.AppliedWorkoutID != nil {
			// Append / merge exercises to existing active workout
			existingWorkoutID := *activeSession.AppliedWorkoutID

			for _, input := range workoutInputs {
				var existingWEID string
				var existingSetsJSON []byte

				// Check if this exercise already exists in this workout
				err := tx.QueryRow(ctx,
					`SELECT id, sets_data FROM workout_exercises WHERE workout_id = $1 AND exercise_id = $2 LIMIT 1`,
					existingWorkoutID, input.ExerciseID,
				).Scan(&existingWEID, &existingSetsJSON)

				if err == nil {
					// Merge / Append sets to existing exercise
					var existingSets []Set
					if err := json.Unmarshal(existingSetsJSON, &existingSets); err != nil {
						return nil, fmt.Errorf("unmarshal existing exercise sets: %w", err)
					}

					startSetNum := len(existingSets) + 1
					for idx := range input.Sets {
						input.Sets[idx].SetNumber = startSetNum + idx
					}
					mergedSets := append(existingSets, input.Sets...)
					mergedSetsJSON, err := json.Marshal(mergedSets)
					if err != nil {
						return nil, fmt.Errorf("marshal merged exercise sets: %w", err)
					}

					if _, err := tx.Exec(ctx,
						`UPDATE workout_exercises SET sets_data = $1 WHERE id = $2`,
						mergedSetsJSON, existingWEID,
					); err != nil {
						return nil, fmt.Errorf("update appended workout exercise: %w", err)
					}

					// Record provenance increment
					corrID := correlation.GetCorrelationID(ctx)
					if _, err := tx.Exec(ctx,
						`INSERT INTO workout_ingestion_increments (correlation_id, workout_id, workout_exercise_id, telegram_update_id, telegram_message_id, operation, summary)
						 VALUES ($1, $2, $3, $4, $5, 'sets_appended', $6)`,
						corrID, existingWorkoutID, existingWEID, updateID, messageID, fmt.Sprintf("Appended %d sets to %s", len(input.Sets), input.ExerciseName),
					); err != nil {
						return nil, fmt.Errorf("record appended sets increment: %w", err)
					}
				} else {
					if !errors.Is(err, pgx.ErrNoRows) {
						return nil, fmt.Errorf("find existing workout exercise: %w", err)
					}
					// Insert new exercise into existing workout
					setsJSON, err := json.Marshal(input.Sets)
					if err != nil {
						return nil, fmt.Errorf("marshal new exercise sets: %w", err)
					}
					var newWEID string
					if err := tx.QueryRow(ctx,
						`INSERT INTO workout_exercises (workout_id, exercise_id, order_index, notes, sets_data)
						 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
						existingWorkoutID, input.ExerciseID, input.OrderIndex, input.Notes, setsJSON,
					).Scan(&newWEID); err != nil {
						return nil, fmt.Errorf("insert appended workout exercise: %w", err)
					}

					// Record provenance increment
					corrID := correlation.GetCorrelationID(ctx)
					if _, err := tx.Exec(ctx,
						`INSERT INTO workout_ingestion_increments (correlation_id, workout_id, workout_exercise_id, telegram_update_id, telegram_message_id, operation, summary)
						 VALUES ($1, $2, $3, $4, $5, 'exercise_added', $6)`,
						corrID, existingWorkoutID, newWEID, updateID, messageID, fmt.Sprintf("Added exercise %s with %d sets", input.ExerciseName, len(input.Sets)),
					); err != nil {
						return nil, fmt.Errorf("record added exercise increment: %w", err)
					}
				}
			}

			if _, err := tx.Exec(ctx,
				`UPDATE workouts SET total_volume = total_volume + $1 WHERE id = $2 AND user_id = $3`,
				totalVolume, existingWorkoutID, userID,
			); err != nil {
				return nil, fmt.Errorf("update appended workout total volume: %w", err)
			}
			workoutModel.ID = existingWorkoutID
		} else {
			// 1. Insert New Workout + Exercises
			if err := p.workoutRepo.CreateTx(ctx, tx, workoutModel, workoutInputs); err != nil {
				return nil, fmt.Errorf("create workout tx: %w", err)
			}
			if activeSession != nil && p.telegramRepo != nil {
				if err := p.telegramRepo.UpdateWorkoutSessionStatusTx(ctx, tx, activeSession.ID, userID, "in_progress", &workoutModel.ID); err != nil {
					return nil, fmt.Errorf("link workout to active session: %w", err)
				}
			}

			// Record provenance increment for initial workout creation
			corrID := correlation.GetCorrelationID(ctx)
			if _, err := tx.Exec(ctx,
				`INSERT INTO workout_ingestion_increments (correlation_id, workout_id, telegram_update_id, telegram_message_id, operation, summary)
				 VALUES ($1, $2, $3, $4, 'workout_created', $5)`,
				corrID, workoutModel.ID, updateID, messageID, fmt.Sprintf("Created workout with %d exercises", len(workoutInputs)),
			); err != nil {
				return nil, fmt.Errorf("record created workout increment: %w", err)
			}
		}

		domainRefs = append([]telegram.DomainReference{{
			Type: "workout",
			ID:   workoutModel.ID,
		}}, domainRefs...)

		corrID := correlation.GetCorrelationID(ctx)

		// 2. Insert Feedback Events
		for _, r := range resolved {
			if r.DraftEx.Difficulty != "" || r.DraftEx.FeedbackRaw != "" {
				fbText := r.DraftEx.FeedbackRaw
				if fbText == "" {
					fbText = r.DraftEx.Difficulty
				}
				metadataJSON, err := json.Marshal(map[string]any{
					"difficulty":   r.DraftEx.Difficulty,
					"feedback_raw": r.DraftEx.FeedbackRaw,
				})
				if err != nil {
					return nil, fmt.Errorf("marshal workout feedback metadata: %w", err)
				}
				if _, err := tx.Exec(ctx,
					`INSERT INTO user_feedback_events (correlation_id, user_id, workout_id, exercise_id, feedback_type, raw_text, metadata)
					 VALUES ($1, $2, $3, $4, 'workout_exercise_feedback', $5, $6)`,
					corrID, userID, workoutModel.ID, r.CanonEx.ID, fbText, metadataJSON,
				); err != nil {
					return nil, fmt.Errorf("record workout feedback: %w", err)
				}
			}
		}

		// 3. Give Reply the committed workout values and action status.
		replyFacts.CurrentState["workout_id"] = workoutModel.ID
		replyFacts.ToolResults = domainRefs
		outboxPayload, err := json.Marshal(telegram.OutboxPayload{
			DraftID:          &draftID,
			IsRich:           p.renderer != nil && p.renderer.GetActiveMode(chatID) == telegram.DraftModeRich,
			Text:             "",
			ReplyContext:     replyFacts,
			ParseMode:        "HTML",
			ReplyToMessageID: &messageID,
			DomainReferences: domainRefs,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal workout outbox payload: %w", err)
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
		if p.telegramRepo != nil {
			if err := p.telegramRepo.CreateDeliveryTx(ctx, tx, deliv); err != nil {
				return nil, fmt.Errorf("create outbox delivery tx: %w", err)
			}

			var lastExID *string
			if len(resolved) > 0 {
				lastExID = &resolved[len(resolved)-1].CanonEx.ID
			}
			if err := p.telegramRepo.UpsertConversationStateTx(ctx, tx, &telegram.ConversationState{
				UserID:               userID,
				ChatID:               chatID,
				FocusExerciseID:      lastExID,
				PendingClarification: nil,
			}); err != nil {
				return nil, fmt.Errorf("update conversation state tx: %w", err)
			}

			respSummary, err := json.Marshal(map[string]any{
				"workout_id": workoutModel.ID,
				"action":     "logged",
				"delivery":   deliv.ID,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal workout response: %w", err)
			}
			if err := p.telegramRepo.CompleteUpdateTx(ctx, tx, updateID, respSummary); err != nil {
				return nil, fmt.Errorf("complete workout update: %w", err)
			}
		}

		if ownsTx {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit workout ingestion tx: %w", err)
			}
			ownsTx = false
		}
	}

	return &telegram.IngestionResponse{
		Success:          true,
		WorkoutID:        workoutModel.ID,
		ReplyContext:     replyFacts,
		DomainReferences: domainRefs,
	}, nil
}
