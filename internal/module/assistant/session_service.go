package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
)

type SessionService struct {
	telegramRepo *telegram.Repository
	renderer     telegram.DraftRenderer
}

func NewSessionService(telegramRepo *telegram.Repository, renderer telegram.DraftRenderer) *SessionService {
	return &SessionService{
		telegramRepo: telegramRepo,
		renderer:     renderer,
	}
}

// CreateProposal creates a new workout session proposal inside a transaction, superseding older proposals.
func (s *SessionService) CreateProposal(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	proposal *WorkoutProposalDraft,
) (*telegram.IngestionResponse, error) {
	draftID := telegram.StableDraftID(updateID)

	proposalBytes, err := json.Marshal(proposal)
	if err != nil {
		return nil, fmt.Errorf("marshal proposal: %w", err)
	}

	sessionModel := &telegram.WorkoutSession{
		UserID:       userID,
		Status:       "proposed",
		ProposalData: proposalBytes,
		ExpiresAt:    time.Now().Add(24 * time.Hour),
	}

	var domainRefs []telegram.DomainReference

	if tx != nil && s.telegramRepo != nil {
		// 0. Explicit User-Scoped Lock to serialize concurrent proposal requests
		if err := s.telegramRepo.LockUserTx(ctx, tx, userID); err != nil {
			return nil, fmt.Errorf("lock user for workout proposal: %w", err)
		}

		// 1. Supersede any existing 'proposed' sessions
		if err := s.telegramRepo.SupersedeActiveWorkoutSessionsTx(ctx, tx, userID); err != nil {
			return nil, fmt.Errorf("supersede active workout proposals: %w", err)
		}

		// 2. Insert new proposed session
		if err := s.telegramRepo.CreateWorkoutSessionTx(ctx, tx, sessionModel); err != nil {
			return nil, fmt.Errorf("create workout session: %w", err)
		}

		domainRefs = append(domainRefs, telegram.DomainReference{
			Type: "workout_session",
			ID:   sessionModel.ID,
		})
		for _, ex := range proposal.Exercises {
			domainRefs = append(domainRefs, telegram.DomainReference{
				Type: "exercise",
				ID:   ex.ExerciseID,
				Name: ex.ExerciseName,
			})
		}

		// 3. Enqueue durable Telegram delivery
		outboxPayload, err := json.Marshal(telegram.OutboxPayload{
			DraftID:          &draftID,
			IsRich:           s.renderer != nil && s.renderer.GetActiveMode(chatID) == telegram.DraftModeRich,
			Text:             "",
			ReplyContext:     &telegram.ReplyContext{EventType: "workout_proposal", ActionsPerformed: []string{"coach.propose_workout"}, CurrentState: map[string]any{"proposal": proposal, "session_status": "proposed"}, ToolResults: domainRefs},
			ParseMode:        "HTML",
			ReplyToMessageID: &messageID,
			DomainReferences: domainRefs,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal workout proposal delivery: %w", err)
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
		if err := s.telegramRepo.CreateDeliveryTx(ctx, tx, deliv); err != nil {
			return nil, fmt.Errorf("create delivery tx: %w", err)
		}

		// 4. Mark update completed
		respSummary, err := json.Marshal(map[string]any{
			"session_id": sessionModel.ID,
			"action":     "proposed_workout",
			"delivery":   deliv.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal workout proposal response: %w", err)
		}
		if err := s.telegramRepo.CompleteUpdateTx(ctx, tx, updateID, respSummary); err != nil {
			return nil, fmt.Errorf("complete workout proposal update: %w", err)
		}
	}

	return &telegram.IngestionResponse{
		Success:          true,
		ReplyContext:     &telegram.ReplyContext{EventType: "workout_proposal", CurrentState: map[string]any{"proposal": proposal}, ToolResults: domainRefs},
		DomainReferences: domainRefs,
	}, nil
}

// ModifyActiveProposal modifies exercises or duration of an active proposal.
func (s *SessionService) ModifyActiveProposal(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	chatID int64,
	updateID int64,
	messageID int64,
	session *telegram.WorkoutSession,
	text string,
) (*telegram.IngestionResponse, error) {
	var proposal WorkoutProposalDraft
	if err := json.Unmarshal(session.ProposalData, &proposal); err != nil {
		return nil, fmt.Errorf("unmarshal existing proposal: %w", err)
	}

	t := strings.ToLower(text)

	// "убери гравитон" / "не хочу сегодня гравитон" / "замени гравитон"
	var updatedExs []ProposedExerciseDraft
	for _, ex := range proposal.Exercises {
		if strings.Contains(t, "гравитон") && strings.Contains(strings.ToLower(ex.ExerciseName), "graviton") {
			continue // Removed
		}
		if strings.Contains(t, "лег пресс") && strings.Contains(strings.ToLower(ex.ExerciseName), "leg press") {
			continue // Removed
		}
		updatedExs = append(updatedExs, ex)
	}
	proposal.Exercises = updatedExs

	// Add substitution if requested (e.g. "добавь пресс")
	if strings.Contains(t, "добавь пресс") || strings.Contains(t, "пресс") {
		reps := 12
		proposal.Exercises = append(proposal.Exercises, ProposedExerciseDraft{
			ExerciseID:   "00000000-0000-0000-0000-000000000006",
			ExerciseName: "Dead Bug",
			RestSec:      60,
			Sets: []ProposedSetDraft{
				{SetNumber: 1, Reps: &reps},
				{SetNumber: 2, Reps: &reps},
				{SetNumber: 3, Reps: &reps},
			},
		})
	}

	proposal.Title = "Обновленный план на сегодня"
	proposal.AdaptationNote = "План скорректирован по твоему пожеланию."

	newBytes, err := json.Marshal(proposal)
	if err != nil {
		return nil, fmt.Errorf("marshal updated workout proposal: %w", err)
	}
	draftID := telegram.StableDraftID(updateID)

	domainRefs := []telegram.DomainReference{
		{Type: "workout_session", ID: session.ID},
	}

	if tx != nil && s.telegramRepo != nil {
		if err := s.telegramRepo.UpdateWorkoutSessionProposalTx(ctx, tx, session.ID, userID, newBytes); err != nil {
			return nil, fmt.Errorf("update proposal tx: %w", err)
		}
		if err := s.telegramRepo.SaveSessionRevisionTx(ctx, tx, session.ID, &updateID, session.ProposalData, newBytes, text); err != nil {
			return nil, fmt.Errorf("save revision tx: %w", err)
		}

		outboxPayload, err := json.Marshal(telegram.OutboxPayload{
			DraftID:          &draftID,
			IsRich:           s.renderer != nil && s.renderer.GetActiveMode(chatID) == telegram.DraftModeRich,
			Text:             "",
			ReplyContext:     &telegram.ReplyContext{EventType: "workout_proposal", ActionsPerformed: []string{"coach.propose_workout"}, CurrentState: map[string]any{"proposal": proposal, "session_status": "proposed"}, ToolResults: domainRefs},
			ParseMode:        "HTML",
			ReplyToMessageID: &messageID,
			DomainReferences: domainRefs,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal modified proposal delivery: %w", err)
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
		if err := s.telegramRepo.CreateDeliveryTx(ctx, tx, deliv); err != nil {
			return nil, fmt.Errorf("create delivery tx: %w", err)
		}

		respSummary, err := json.Marshal(map[string]any{
			"session_id": session.ID,
			"action":     "modified_proposal",
			"delivery":   deliv.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal modified proposal response: %w", err)
		}
		if err := s.telegramRepo.CompleteUpdateTx(ctx, tx, updateID, respSummary); err != nil {
			return nil, fmt.Errorf("complete modified proposal update: %w", err)
		}
	}

	return &telegram.IngestionResponse{
		Success:          true,
		ReplyContext:     &telegram.ReplyContext{EventType: "workout_proposal", CurrentState: map[string]any{"proposal": proposal}, ToolResults: domainRefs},
		DomainReferences: domainRefs,
	}, nil
}

// StartActiveSession transitions active session to in_progress.
func (s *SessionService) StartActiveSession(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	session *telegram.WorkoutSession,
) error {
	if tx != nil && s.telegramRepo != nil {
		return s.telegramRepo.UpdateWorkoutSessionStatusTx(ctx, tx, session.ID, userID, "in_progress", nil)
	}
	return nil
}

// CompleteActiveSession transitions active session to completed.
func (s *SessionService) CompleteActiveSession(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	session *telegram.WorkoutSession,
	workoutID *string,
) error {
	if tx != nil && s.telegramRepo != nil {
		return s.telegramRepo.UpdateWorkoutSessionStatusTx(ctx, tx, session.ID, userID, "completed", workoutID)
	}
	return nil
}

// AbandonActiveSession transitions active session to abandoned.
func (s *SessionService) AbandonActiveSession(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	session *telegram.WorkoutSession,
) error {
	if tx != nil && s.telegramRepo != nil {
		return s.telegramRepo.UpdateWorkoutSessionStatusTx(ctx, tx, session.ID, userID, "abandoned", nil)
	}
	return nil
}
