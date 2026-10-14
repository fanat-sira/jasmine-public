package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"jasmine/internal/module/telegram"
)

type pendingImageIntent struct {
	Classification ImageClassificationDraft `json:"classification"`
	Caption        string                   `json:"caption"`
	ImageUpdateID  int64                    `json:"image_update_id"`
	ImageMessageID int64                    `json:"image_message_id"`
	FileID         string                   `json:"file_id"`
	FileUniqueID   string                   `json:"file_unique_id"`
	CorrelationID  string                   `json:"correlation_id"`
}

func (o *Orchestrator) askImageIntent(ctx context.Context, userID string, chatID int64, turn MultimodalTurn, c ImageClassificationDraft, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	if o.telegramRepo == nil {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "image_intent_needed", Clarification: []string{"image_intent_needed"}, CurrentState: map[string]any{"classification": c}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	state, err := o.telegramRepo.GetConversationState(ctx, userID)
	if err != nil && err != telegram.ErrNotFound {
		return nil, err
	}
	if state == nil {
		state = &telegram.ConversationState{UserID: userID, ChatID: chatID}
	}
	if state.PendingClarification != nil && state.PendingClarification.Type != "image_unknown_needs_intent" {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "pending_clarification", Clarification: []string{state.PendingClarification.Type}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	pending := pendingImageIntent{Classification: c, Caption: turn.Text, ImageUpdateID: turn.UpdateID, ImageMessageID: turn.MessageID, FileID: turn.Attachment.FileID, FileUniqueID: turn.Attachment.FileUniqueID, CorrelationID: turn.CorrelationID}
	b, err := json.Marshal(pending)
	if err != nil {
		return nil, err
	}
	state.PendingClarification = &telegram.PendingClarification{Type: "image_unknown_needs_intent", OriginalQuery: turn.Text, Context: b, CreatedAt: time.Now()}
	if err := o.telegramRepo.UpsertConversationState(ctx, state); err != nil {
		return nil, err
	}
	return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "image_intent_needed", Clarification: []string{"image_intent_needed"}, CurrentState: map[string]any{"classification": c}, ToolResults: refs}, DomainReferences: refs}, nil
}
func (o *Orchestrator) continueImageIntent(ctx context.Context, userID string, chatID int64, clar *telegram.PendingClarification, text string) (*telegram.IngestionResponse, error) {
	var pending pendingImageIntent
	if err := json.Unmarshal(clar.Context, &pending); err != nil {
		return nil, fmt.Errorf("decode image intent: %w", err)
	}
	state, err := o.telegramRepo.GetConversationState(ctx, userID)
	if err != nil {
		return nil, err
	}
	state.PendingClarification = nil
	if err := o.telegramRepo.UpsertConversationState(ctx, state); err != nil {
		return nil, err
	}
	refs := []telegram.DomainReference{{Type: "image_workflow", Kind: string(pending.Classification.Type), Extra: map[string]any{"source_update_id": pending.ImageUpdateID}}}
	return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "prior_image_unavailable", ExternalContent: pending.Classification.Evidence, CurrentState: map[string]any{"classification_type": pending.Classification.Type, "image_not_retained": true}, ToolResults: refs}, DomainReferences: refs}, nil
}
