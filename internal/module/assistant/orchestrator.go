package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/prompts"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"jasmine/internal/pkg/correlation"
	"jasmine/internal/pkg/identity"
)

type Orchestrator struct {
	db                *pgxpool.Pool
	aiClient          *openrouter.Client
	model             string
	replyModel        string
	ingestionPipe     *workout.IngestionPipeline
	correctionSvc     *workout.CorrectionService
	memoryExtractor   *MemoryExtractor
	knowledge         *PersonalKnowledge
	preferences       *AssistantPreferences
	contextBuilder    *ContextBuilder
	candidateSelector *CandidateSelector
	generator         *WorkoutGenerator
	requestExtractor  *WorkoutRequestExtractor
	sessionService    *SessionService
	telegramRepo      *telegram.Repository
	renderer          telegram.DraftRenderer
	imageAnalyzer     *ImageAnalyzer
	coachAgent        *CoachAgent
	prompts           prompts.Store
}

func NewOrchestrator(
	db *pgxpool.Pool,
	aiClient *openrouter.Client,
	model string,
	replyModel string,
	ingestionPipe *workout.IngestionPipeline,
	correctionSvc *workout.CorrectionService,
	memoryExtractor *MemoryExtractor,
	contextBuilder *ContextBuilder,
	candidateSelector *CandidateSelector,
	generator *WorkoutGenerator,
	sessionService *SessionService,
	telegramRepo *telegram.Repository,
	renderer telegram.DraftRenderer,
) *Orchestrator {
	o := &Orchestrator{
		db:                db,
		aiClient:          aiClient,
		model:             model,
		replyModel:        replyModel,
		ingestionPipe:     ingestionPipe,
		correctionSvc:     correctionSvc,
		memoryExtractor:   memoryExtractor,
		knowledge:         NewPersonalKnowledge(db, aiClient, model),
		preferences:       NewAssistantPreferences(db, aiClient, model),
		contextBuilder:    contextBuilder,
		candidateSelector: candidateSelector,
		generator:         generator,
		requestExtractor:  NewWorkoutRequestExtractor(),
		sessionService:    sessionService,
		telegramRepo:      telegramRepo,
		renderer:          renderer,
	}
	if db != nil && telegramRepo != nil {
		o.coachAgent = NewCoachAgent(o, db)
	}
	return o
}

func (o *Orchestrator) SetPromptStore(store prompts.Store) {
	o.prompts = store
	if o.coachAgent != nil {
		o.coachAgent.SetPromptStore(store)
	}
	if o.knowledge != nil {
		o.knowledge.SetPromptStore(store)
	}
	if o.imageAnalyzer != nil {
		o.imageAnalyzer.SetPromptStore(store)
	}
}

func (o *Orchestrator) SetImageAnalyzer(a *ImageAnalyzer) {
	o.imageAnalyzer = a
	if a != nil && o.prompts != nil {
		a.SetPromptStore(o.prompts)
	}
}
func (o *Orchestrator) SetCoachPlanner(p CoachPlanner) {
	if o.coachAgent != nil {
		o.coachAgent.SetPlanner(p)
	}
}
func (o *Orchestrator) IngestImage(ctx context.Context, userID string, chatID, updateID, messageID int64, replyToMessageID *int64, caption string, image telegram.ImageInput) (*telegram.IngestionResponse, error) {
	ctx = openrouter.WithSessionID(ctx, conversationSessionID(chatID))
	if o.coachAgent != nil {
		return o.coachAgent.ProcessImage(ctx, CoachImageObservation{UserID: userID, ChatID: chatID, UpdateID: updateID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Caption: caption, Image: image})
	}
	return o.ingestImageObservation(ctx, userID, chatID, updateID, messageID, replyToMessageID, caption, image, nil)
}

func (o *Orchestrator) ingestImageObservation(ctx context.Context, userID string, chatID, updateID, messageID int64, replyToMessageID *int64, caption string, image telegram.ImageInput, coach *CoachContext) (*telegram.IngestionResponse, error) {
	if o.imageAnalyzer == nil {
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "image_analysis_unavailable", Errors: []string{"photo_model_unavailable"}}}, nil
	}
	turn := MultimodalTurn{Text: caption, UpdateID: updateID, MessageID: messageID, CorrelationID: correlation.GetCorrelationID(ctx), Attachment: ImageAttachment{FileID: image.FileID, FileUniqueID: image.FileUniqueID, MIMEType: image.MIMEType, ByteSize: int64(len(image.Bytes)), Bytes: image.Bytes}}
	if coach != nil {
		turn.Timezone = coach.Timezone
		turn.RecentMessages = coach.RecentMessages
		if coach.PendingClarification != nil {
			turn.ConversationContext = "Ожидается уточнение: " + coach.PendingClarification.Type
		}
		if coach.Session != nil {
			turn.ConversationContext += "; активная тренировка: " + coach.Session.Status
		}
		if coach.Current != nil {
			turn.ConversationContext += "; следующее упражнение: " + coach.Current.ExerciseName
		}
	} else if o.telegramRepo != nil {
		if localNow, tzErr := o.imageLocalTime(ctx, userID, ""); tzErr == nil {
			turn.Timezone = localNow.Location().String()
		}
		state, err := o.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, err
		}
		if state != nil && state.PendingClarification != nil {
			turn.ConversationContext = "Ожидается уточнение: " + state.PendingClarification.Type
		}
	}
	if isMealEstimateFollowup(caption) {
		answer, err := o.imageAnalyzer.MealFollowup(ctx, turn)
		if err != nil {
			return nil, err
		}
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "meal_photo_followup", ExternalContent: []string{answer}}, DomainReferences: []telegram.DomainReference{{Type: "image_workflow", Kind: "meal_followup", Extra: map[string]any{"file_unique_id": image.FileUniqueID}}}}, nil
	}
	classification, err := o.imageAnalyzer.Classify(ctx, turn)
	if err != nil {
		return nil, fmt.Errorf("classify image: %w", err)
	}
	ref := []telegram.DomainReference{{Type: "image_workflow", Kind: string(classification.Type), Extra: map[string]any{"confidence": classification.Confidence, "file_unique_id": image.FileUniqueID}}}
	if classification.Confidence < 0.65 || classification.RequiresClarification || classification.Type == ImageUnknown {
		if strings.Contains(caption, "?") {
			answer, err := o.imageAnalyzer.Other(ctx, turn)
			if err != nil {
				return nil, err
			}
			return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "image_observation", ExternalContent: []string{answer}, ToolResults: ref}, DomainReferences: ref}, nil
		}
		return o.askImageIntent(ctx, userID, chatID, turn, classification, ref)
	}
	switch classification.Type {
	case ImageFood:
		draft, err := o.imageAnalyzer.Food(ctx, turn)
		if err != nil {
			return nil, fmt.Errorf("analyze food: %w", err)
		}
		return o.saveImageMeal(ctx, userID, chatID, updateID, messageID, caption, image, draft, ref)
	case ImageNutritionLabel:
		draft, err := o.imageAnalyzer.Label(ctx, turn)
		if err != nil {
			return nil, fmt.Errorf("analyze label: %w", err)
		}
		if draft.Calories == nil || draft.ProteinG == nil || draft.FatG == nil || draft.CarbsG == nil {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "nutrition_label_incomplete", Clarification: []string{"nutrition_values_not_visible"}, CurrentState: map[string]any{"label": draft}}, DomainReferences: ref}, nil
		}
		ref[0].Extra["source"] = draft.Source
		ref[0].Extra["draft"] = draft
		ref[0].Extra["source_message_id"] = messageID
		ref[0].Extra["caption"] = caption
		if wantsSaveProduct(caption) {
			return o.saveLabelProduct(ctx, userID, chatID, updateID, messageID, image, draft, ref)
		}
		if wantsLogLabelMeal(caption) {
			return o.saveLabelMeal(ctx, userID, chatID, updateID, messageID, messageID, caption, image, draft, ref)
		}
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "nutrition_label_observed", CurrentState: map[string]any{"label": draft}, ToolResults: ref}, DomainReferences: ref}, nil
	case ImageScale:
		draft, err := o.imageAnalyzer.Scale(ctx, turn)
		if err != nil {
			return nil, err
		}
		if draft.WeightKG == nil || draft.Confidence < 0.8 {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "scale_reading_unclear", Clarification: []string{"weight_not_readable"}}, DomainReferences: ref}, nil
		}
		return o.saveImageScale(ctx, userID, chatID, updateID, messageID, caption, image, draft, ref)
	case ImageGymDisplay:
		draft, err := o.imageAnalyzer.Gym(ctx, turn)
		if err != nil {
			return nil, err
		}
		if !validMachineDraft(draft) {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "machine_reading_unclear", Clarification: []string{"metrics_not_readable"}}, DomainReferences: ref}, nil
		}
		pending := pendingMachineImage{Draft: *draft, Caption: caption, ImageUpdateID: updateID, ImageMessageID: messageID, FileUniqueID: image.FileUniqueID, FileID: image.FileID, CorrelationID: correlation.GetCorrelationID(ctx)}
		name := machineFromCaption(caption)
		if name == "" {
			return o.saveMachinePending(ctx, userID, chatID, pending, ref)
		}
		res, err := o.logMachine(ctx, userID, chatID, updateID, messageID, name, pending, ref)
		if errors.Is(err, workout.ErrMachineUnresolved) {
			return o.saveMachinePending(ctx, userID, chatID, pending, ref)
		}
		if err != nil {
			return nil, err
		}
		return res, nil
	case ImageBodyProgress:
		return o.askImageIntent(ctx, userID, chatID, turn, classification, ref)
	case ImageFitnessEquipment:
		draft, err := o.imageAnalyzer.Equipment(ctx, turn)
		if err != nil {
			return nil, err
		}
		if draft.Confidence < 0.65 || strings.TrimSpace(draft.Name) == "" {
			return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "equipment_unclear", Clarification: []string{"equipment_name_needed"}, ExternalContent: []string{draft.Name, draft.Purpose}}, DomainReferences: ref}, nil
		}
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "equipment_observed", ExternalContent: []string{draft.Name, draft.Purpose}, ToolResults: ref}, DomainReferences: ref}, nil
	case ImageDocument:
		answer, err := o.imageAnalyzer.Document(ctx, turn)
		if err != nil {
			return nil, err
		}
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "image_observation", ExternalContent: []string{answer}, ToolResults: ref}, DomainReferences: ref}, nil
	case ImageOther:
		answer, err := o.imageAnalyzer.Other(ctx, turn)
		if err != nil {
			return nil, err
		}
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "image_observation", ExternalContent: []string{answer}, ToolResults: ref}, DomainReferences: ref}, nil
	default:
		return o.askImageIntent(ctx, userID, chatID, turn, classification, ref)
	}
}

func (o *Orchestrator) IngestMessage(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	replyToMessageID *int64,
	rawText string,
) (*telegram.IngestionResponse, error) {
	return o.ProcessMessage(ctx, userID, chatID, updateID, messageID, replyToMessageID, rawText)
}

// ProcessMessage interprets and executes single or multi-action conversational messages
// within a single atomic PostgreSQL transaction.
func (o *Orchestrator) ProcessMessage(
	ctx context.Context,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	replyToMessageID *int64,
	text string,
) (*telegram.IngestionResponse, error) {
	ctx = identity.WithUserID(ctx, userID)
	ctx = openrouter.WithSessionID(ctx, conversationSessionID(chatID))
	ctx = correlation.WithOperation(ctx, "orchestrator_action_dispatch")

	if raw, ok := profileMarkdownText(text); ok {
		return o.saveProfileMarkdown(ctx, userID, chatID, updateID, messageID, raw)
	}
	if raw, ok := profileJSONText(text); ok {
		return o.saveProfileJSON(ctx, userID, chatID, updateID, messageID, raw)
	}
	if isDailyMealsQuestion(text) {
		return o.answerDailyMeals(ctx, userID, chatID, updateID, messageID)
	}
	if correction, ok, err := o.mealCorrectionRequest(ctx, chatID, text); err != nil {
		return nil, err
	} else if ok {
		return o.correctMealCalories(ctx, userID, chatID, updateID, messageID, correction)
	}

	// 1. Strict Priority Clarification Precedence
	var pendingClar *telegram.PendingClarification
	if o.telegramRepo != nil {
		state, err := o.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, fmt.Errorf("get conversation state: %w", err)
		}
		if state != nil && state.PendingClarification != nil {
			pendingClar = state.PendingClarification
		}
	}

	if pendingMatches(pendingClar, text, replyToMessageID) {
		if pendingClar.Type == "image_unknown_needs_intent" {
			return o.continueImageIntent(ctx, userID, chatID, pendingClar, text)
		}
		if pendingClar.Type == "machine_display_needs_machine" {
			var pending pendingMachineImage
			if err := json.Unmarshal(pendingClar.Context, &pending); err != nil {
				return nil, fmt.Errorf("decode pending machine display: %w", err)
			}
			return o.logMachine(ctx, userID, chatID, updateID, messageID, text, pending, []telegram.DomainReference{{Type: "image_workflow", Kind: "gym_machine_display"}})
		}
		if pendingClar.Type == "exercise_disambiguation" && o.ingestionPipe != nil {
			res, err := o.ingestionPipe.ContinueClarification(ctx, userID, chatID, updateID, messageID, text, pendingClar)
			if err != nil {
				return nil, fmt.Errorf("continue exercise clarification: %w", err)
			}
			if res != nil {
				return res, nil
			}
		} else if pendingClar.Type == "correction_disambiguation" && o.correctionSvc != nil {
			res, err := o.correctionSvc.ProcessCorrection(ctx, userID, chatID, updateID, messageID, replyToMessageID, text)
			if err != nil {
				return nil, fmt.Errorf("continue correction clarification: %w", err)
			}
			if res != nil {
				return res, nil
			}
		}
	}

	if o.coachAgent != nil {
		res, err := o.coachAgent.Process(ctx, userID, chatID, updateID, messageID, replyToMessageID, text)
		if err != nil {
			return nil, fmt.Errorf("coach agent: %w", err)
		}
		if res != nil {
			return res, nil
		}
	}

	// 2. Open Atomic Unit-of-Work Transaction
	var tx pgx.Tx
	var err error
	if o.db != nil {
		tx, err = o.db.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin orchestrator tx: %w", err)
		}
		defer func() {
			if tx != nil {
				_ = tx.Rollback(ctx)
			}
		}()
	}

	// 3. Extract Memory Candidates (Preferences, equipment, transient state)
	var memoryCandidates []MemoryCandidate
	if o.memoryExtractor != nil {
		memoryCandidates, _ = o.memoryExtractor.ExtractCandidates(ctx, text, time.Local)
	}

	// 4. Check active workout session for lifecycle actions (modifications, start, completion)
	var activeSession *telegram.WorkoutSession
	if o.telegramRepo != nil {
		activeSession, err = o.telegramRepo.GetActiveWorkoutSession(ctx, userID)
		if err != nil && !errors.Is(err, telegram.ErrNotFound) {
			return nil, fmt.Errorf("get active workout session: %w", err)
		}
	}

	tLower := strings.ToLower(strings.TrimSpace(text))

	// 5. Try Workout Correction Action
	if o.correctionSvc != nil {
		corrRes, err := o.correctionSvc.ProcessCorrection(ctx, userID, chatID, updateID, messageID, replyToMessageID, text)
		if err != nil && !errors.Is(err, workout.ErrNotACorrection) {
			return nil, fmt.Errorf("process workout correction: %w", err)
		}
		if corrRes != nil {
			if len(memoryCandidates) > 0 {
				if err := o.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, memoryCandidates); err != nil {
					return nil, fmt.Errorf("persist correction memory candidates: %w", err)
				}
			}
			if tx != nil {
				if err := tx.Commit(ctx); err != nil {
					return nil, fmt.Errorf("commit correction tx: %w", err)
				}
				tx = nil
			}
			return corrRes, nil
		}
	}

	// 6. Try Workout Logging Action
	if workout.IsPlausibleWorkoutLog(text) && o.ingestionPipe != nil {
		logRes, err := o.ingestionPipe.IngestWorkoutTx(ctx, tx, userID, chatID, updateID, messageID, text)
		if err != nil {
			return nil, fmt.Errorf("ingest workout: %w", err)
		}
		if logRes != nil {
			if len(memoryCandidates) > 0 {
				if err := o.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, memoryCandidates); err != nil {
					return nil, fmt.Errorf("persist workout memory candidates: %w", err)
				}
			}
			// If active session was in_progress or proposed, keep in_progress and record applied workout
			if activeSession != nil && logRes.WorkoutID != "" && o.telegramRepo != nil {
				if activeSession.AppliedWorkoutID == nil {
					if err := o.telegramRepo.UpdateWorkoutSessionStatusTx(ctx, tx, activeSession.ID, userID, "in_progress", &logRes.WorkoutID); err != nil {
						return nil, fmt.Errorf("link logged workout to active session: %w", err)
					}
				}
			}
			if tx != nil {
				if err := tx.Commit(ctx); err != nil {
					return nil, fmt.Errorf("commit workout ingestion tx: %w", err)
				}
				tx = nil
			}
			return logRes, nil
		}
	}

	// 7. Workout Generation Requests ("что сегодня делать?", "составь тренировку на 40 мин")
	if IsWorkoutGenerationRequest(text) && o.generator != nil {
		// If a session is already in_progress, do not silently destroy it
		if activeSession != nil && activeSession.Status == "in_progress" && !strings.Contains(tLower, "отмени") && !strings.Contains(tLower, "заново") {
			res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", []telegram.DomainReference{
				{Type: "workout_session", ID: activeSession.ID},
			}, &telegram.ReplyContext{EventType: "workout_already_active", CurrentState: map[string]any{"session_id": activeSession.ID, "status": activeSession.Status}})
			if err != nil {
				return nil, err
			}
			if tx != nil {
				if err := tx.Commit(ctx); err != nil {
					return nil, fmt.Errorf("commit active-workout notice tx: %w", err)
				}
				tx = nil
			}
			return res, nil
		}

		req := o.requestExtractor.ExtractRequest(ctx, text)

		var allMemories []telegram.UserMemory
		var allMessages []telegram.TelegramMessage
		var feedbackEvents []telegram.UserFeedbackEvent
		if o.telegramRepo != nil {
			allMemories, err = o.telegramRepo.ListMemories(ctx, userID, nil, nil)
			if err != nil {
				return nil, fmt.Errorf("list workout memories: %w", err)
			}
			allMessages, err = o.telegramRepo.ListMessages(ctx, chatID, 15)
			if err != nil {
				return nil, fmt.Errorf("list conversation messages: %w", err)
			}
			feedbackEvents, err = o.telegramRepo.ListFeedbackEvents(ctx, userID, nil, 10)
			if err != nil {
				return nil, fmt.Errorf("list workout feedback: %w", err)
			}
		}

		var convCtx *ConversationContext
		if o.contextBuilder != nil {
			convCtx = o.contextBuilder.BuildContext(
				ctx,
				ActionWorkoutRequest,
				userID,
				nil,
				allMemories,
				allMessages,
				nil,
				feedbackEvents,
				req.RequestedFocus,
			)
		}

		candidates, err := o.candidateSelector.SelectCandidates(ctx, req, allMemories, nil, feedbackEvents)
		if err != nil {
			return nil, fmt.Errorf("select candidates: %w", err)
		}

		proposal, err := o.generator.GenerateProposal(ctx, req, convCtx, candidates)
		if err != nil {
			return nil, fmt.Errorf("generate proposal: %w", err)
		}

		if o.sessionService != nil {
			res, err := o.sessionService.CreateProposal(ctx, tx, userID, chatID, updateID, messageID, proposal)
			if err != nil {
				return nil, fmt.Errorf("create session proposal: %w", err)
			}
			if tx != nil {
				if err := tx.Commit(ctx); err != nil {
					return nil, fmt.Errorf("commit workout proposal tx: %w", err)
				}
				tx = nil
			}
			return res, nil
		}
	}

	// 8. Standalone Memory Updates
	if len(memoryCandidates) > 0 {
		if err := o.persistMemoryCandidatesTx(ctx, tx, userID, updateID, messageID, memoryCandidates); err != nil {
			return nil, fmt.Errorf("persist memory candidates: %w", err)
		}
		res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, &telegram.ReplyContext{EventType: "memory_saved", ActionsPerformed: []string{"memory.save"}, CurrentState: map[string]any{"memories": memoryCandidates}})
		if err != nil {
			return nil, err
		}
		if tx != nil {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit memory tx: %w", err)
			}
			tx = nil
		}
		return res, nil
	}

	// 9. Conversational Fallback
	res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, &telegram.ReplyContext{EventType: "general_chat"})
	if err != nil {
		return nil, err
	}
	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit conversational tx: %w", err)
		}
		tx = nil
	}
	return res, nil
}

func (o *Orchestrator) persistMemoryCandidatesTx(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	updateID int64,
	messageID int64,
	candidates []MemoryCandidate,
) error {
	if o.telegramRepo == nil || len(candidates) == 0 {
		return nil
	}

	if o.memoryExtractor != nil {
		candidates = o.memoryExtractor.CanonicalizeCandidates(ctx, candidates)
	}

	for _, c := range candidates {
		if c.IsReversal {
			if err := o.telegramRepo.DeactivateMemoryTx(ctx, tx, userID, c.Category, c.Key); err != nil {
				return fmt.Errorf("deactivate memory %s/%s: %w", c.Category, c.Key, err)
			}
			continue
		}

		valJSON, err := json.Marshal(c.Value)
		if err != nil {
			return fmt.Errorf("marshal memory %s/%s: %w", c.Category, c.Key, err)
		}
		mem := &telegram.UserMemory{
			UserID:          userID,
			Category:        c.Category,
			Scope:           c.Scope,
			Key:             c.Key,
			Value:           valJSON,
			Confidence:      c.Confidence,
			ValidUntil:      c.ValidUntil,
			SourceUpdateID:  &updateID,
			SourceMessageID: &messageID,
			SourceText:      c.SourceText,
		}

		if tx != nil {
			if err := o.telegramRepo.SaveMemoryTx(ctx, tx, mem); err != nil {
				return fmt.Errorf("save memory %s/%s: %w", c.Category, c.Key, err)
			}
			if mem.ID == "" {
				return fmt.Errorf("save memory %s/%s: empty memory ID", c.Category, c.Key)
			}
			if err := o.telegramRepo.SupersedeMemoryTx(ctx, tx, userID, c.Category, c.Key, mem.ID); err != nil {
				return fmt.Errorf("supersede memory %s/%s: %w", c.Category, c.Key, err)
			}
		} else {
			if err := o.telegramRepo.SaveMemory(ctx, mem); err != nil {
				return fmt.Errorf("save memory %s/%s: %w", c.Category, c.Key, err)
			}
			if mem.ID == "" {
				return fmt.Errorf("save memory %s/%s: empty memory ID", c.Category, c.Key)
			}
			if err := o.telegramRepo.SupersedeMemoryTx(ctx, nil, userID, c.Category, c.Key, mem.ID); err != nil {
				return fmt.Errorf("supersede memory %s/%s: %w", c.Category, c.Key, err)
			}
		}
	}
	return nil
}

func (o *Orchestrator) deliverStandAloneResponseTx(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	replyHTML string,
	domainRefs []telegram.DomainReference,
	replyContext ...*telegram.ReplyContext,
) (*telegram.IngestionResponse, error) {
	draftID := telegram.StableDraftID(updateID)
	facts := &telegram.ReplyContext{EventType: "domain_result", ToolResults: domainRefs}
	if len(replyContext) > 0 && replyContext[0] != nil {
		facts = replyContext[0]
	}

	if tx != nil && o.telegramRepo != nil {
		outboxPayload, err := json.Marshal(telegram.OutboxPayload{
			DraftID:          &draftID,
			IsRich:           o.renderer != nil && o.renderer.GetActiveMode(chatID) == telegram.DraftModeRich,
			Text:             "",
			ReplyContext:     facts,
			ParseMode:        "HTML",
			ReplyToMessageID: &messageID,
			DomainReferences: domainRefs,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal standalone response delivery: %w", err)
		}

		deliv := &telegram.TelegramDelivery{
			UpdateID:      &updateID,
			ChatID:        chatID,
			UserID:        userID,
			CorrelationID: correlation.GetCorrelationID(ctx),
			Status:        telegram.DeliveryStatusPending,
			Payload:       outboxPayload,
			Attempts:      0,
		}
		if err := o.telegramRepo.CreateDeliveryTx(ctx, tx, deliv); err != nil {
			return nil, fmt.Errorf("create standalone response delivery: %w", err)
		}

		respSummary, err := json.Marshal(map[string]any{
			"action":   "conversational_response",
			"delivery": deliv.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal standalone response: %w", err)
		}
		if err := o.telegramRepo.CompleteUpdateTx(ctx, tx, updateID, respSummary); err != nil {
			return nil, fmt.Errorf("complete standalone response update: %w", err)
		}
	}

	return &telegram.IngestionResponse{
		Success:          true,
		ConfirmationHTML: replyHTML,
		ReplyContext:     facts,
		DomainReferences: domainRefs,
	}, nil
}

func (o *Orchestrator) BackfillPersonalProfiles(ctx context.Context) error {
	if o.knowledge == nil {
		return nil
	}
	return o.knowledge.BackfillProfiles(ctx)
}

func (o *Orchestrator) PersonalKnowledge() *PersonalKnowledge { return o.knowledge }
