package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	telegramclient "jasmine/internal/clients/telegram"
)

// ReplyContext is the trusted handoff from application state to the sole
// user-facing language model. Facts are source material, never final wording.
type ReplyContext struct {
	UserMessage      string            `json:"user_message,omitempty"`
	ContextPolicy    TurnContextPolicy `json:"context_policy"`
	ExternalContent  []string          `json:"external_content,omitempty"`
	EventType        string            `json:"event_type"`
	Facts            []string          `json:"facts,omitempty"`
	ActionsPerformed []string          `json:"actions_performed,omitempty"`
	ToolResults      []DomainReference `json:"tool_results,omitempty"`
	Clarification    []string          `json:"clarification,omitempty"`
	Errors           []string          `json:"errors,omitempty"`
	CurrentState     map[string]any    `json:"current_state,omitempty"`
}

type OutboxPayload struct {
	DraftID          *int              `json:"draft_id,omitempty"`
	IsRich           bool              `json:"is_rich,omitempty"`
	Text             string            `json:"text"`
	ReplyContext     *ReplyContext     `json:"reply_context,omitempty"`
	ParseMode        string            `json:"parse_mode,omitempty"`
	ReplyToMessageID *int64            `json:"reply_to_message_id,omitempty"`
	DomainReferences []DomainReference `json:"domain_references,omitempty"`
}

type ReplyComposer interface {
	Compose(ctx context.Context, chatID int64, facts ReplyContext, history []TelegramMessage) (string, error)
}

type DeliveryWorker struct {
	replyComposer ReplyComposer
	repo          *Repository
	client        *telegramclient.Client
	log           *slog.Logger
	workerID      string
	maxAttempts   int
	lockTimeout   time.Duration
}

func NewDeliveryWorker(repo *Repository, client *telegramclient.Client, log *slog.Logger) *DeliveryWorker {
	if log == nil {
		log = slog.Default()
	}
	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
	return &DeliveryWorker{
		repo:        repo,
		client:      client,
		log:         log,
		workerID:    workerID,
		maxAttempts: 5,
		lockTimeout: 60 * time.Second,
	}
}

func (w *DeliveryWorker) SetReplyComposer(composer ReplyComposer) { w.replyComposer = composer }

// ProcessDelivery processes a single claimed delivery record.
func (w *DeliveryWorker) ProcessDelivery(ctx context.Context, delivery *TelegramDelivery) error {
	if w.client == nil {
		return telegramclient.ErrNotConfigured
	}

	var payload OutboxPayload
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		if w.repo != nil {
			if markErr := w.repo.MarkDeliveryFailed(ctx, delivery.ID, fmt.Sprintf("unmarshal payload: %v", err)); markErr != nil {
				return fmt.Errorf("unmarshal delivery payload: %w; mark delivery failed: %v", err, markErr)
			}
		}
		return fmt.Errorf("unmarshal delivery payload: %w", err)
	}

	if w.replyComposer == nil {
		return w.failDelivery(ctx, delivery, fmt.Errorf("LLM_REPLY composer is not configured"))
	}
	var history []TelegramMessage
	if w.repo != nil {
		var err error
		if payload.ReplyToMessageID != nil {
			history, err = w.repo.GetRecentMessages(ctx, delivery.ChatID, 51, *payload.ReplyToMessageID)
		} else {
			history, err = w.repo.GetRecentMessages(ctx, delivery.ChatID, 51)
		}
		if err != nil {
			return w.failDelivery(ctx, delivery, fmt.Errorf("load reply history: %w", err))
		}
	}
	facts := ReplyContext{EventType: "legacy_delivery", ToolResults: payload.DomainReferences}
	if payload.ReplyContext != nil {
		facts = *payload.ReplyContext
	}
	prior := make([]TelegramMessage, 0, len(history))
	for _, item := range history {
		if payload.ReplyToMessageID != nil && item.Direction == DirectionInbound && item.MessageID == *payload.ReplyToMessageID {
			if item.Text != nil {
				facts.UserMessage = *item.Text
			}
			continue
		}
		prior = append(prior, item)
	}
	facts.ContextPolicy = ParseTurnContextPolicy(facts.UserMessage)
	if !facts.ContextPolicy.UseConversationHistory || !facts.ContextPolicy.UseDomainHistory {
		prior = nil
	}
	if len(prior) > 50 {
		prior = prior[len(prior)-50:]
	}
	composed, composeErr := w.replyComposer.Compose(ctx, delivery.ChatID, facts, prior)
	if composeErr != nil {
		return w.failDelivery(ctx, delivery, fmt.Errorf("compose reply: %w", composeErr))
	}
	if strings.TrimSpace(composed) == "" {
		return w.failDelivery(ctx, delivery, fmt.Errorf("LLM_REPLY returned empty output"))
	}
	payload.Text = composed
	payload.ParseMode = "HTML"
	payload.Text = telegramclient.NormalizeBotText(payload.Text)
	opts := &telegramclient.SendMessageOptions{
		ParseMode:        payload.ParseMode,
		ReplyToMessageID: payload.ReplyToMessageID,
	}
	if opts.ParseMode == "" {
		opts.ParseMode = "HTML"
	}

	var sentMsg *telegramclient.Message
	var err error

	// If rich draft was used, finalize with sendRichMessage (Bot API 10.1+)
	if payload.IsRich && payload.DraftID != nil {
		sentMsg, err = w.client.SendRichMessage(ctx, delivery.ChatID, *payload.DraftID, payload.Text, opts)
		if err != nil {
			w.log.Warn("sendRichMessage failed, falling back to sendMessage",
				slog.String("delivery_id", delivery.ID),
				slog.String("error", err.Error()),
			)
			sentMsg, err = w.client.SendMessage(ctx, delivery.ChatID, payload.Text, opts)
		}
	} else {
		sentMsg, err = w.client.SendMessage(ctx, delivery.ChatID, payload.Text, opts)
	}

	if err != nil {
		w.logger().Warn("telegram send delivery failed",
			slog.String("delivery_id", delivery.ID),
			slog.Int64("chat_id", delivery.ChatID),
			slog.String("error", err.Error()),
		)
		if w.repo != nil {
			if markErr := w.repo.MarkDeliveryFailed(ctx, delivery.ID, err.Error()); markErr != nil {
				return fmt.Errorf("send telegram delivery: %w; mark delivery failed: %v", err, markErr)
			}
		}
		return err
	}

	// 1. Persist real outbound message in telegram_messages if repo is configured
	if w.repo != nil {
		rawMsgJSON, err := json.Marshal(sentMsg)
		if err != nil {
			return fmt.Errorf("marshal outbound telegram message: %w", err)
		}
		outboundMsg := &TelegramMessage{
			ChatID:           delivery.ChatID,
			MessageID:        sentMsg.MessageID,
			CorrelationID:    delivery.CorrelationID,
			TelegramUserID:   nil, // Outbound from bot
			UserID:           delivery.UserID,
			Direction:        DirectionOutbound,
			ReplyToMessageID: payload.ReplyToMessageID,
			Text:             &payload.Text,
			RawPayload:       rawMsgJSON,
			DomainReferences: payload.DomainReferences,
			CreatedAt:        time.Unix(sentMsg.Date, 0),
		}
		if err := w.repo.SaveMessage(ctx, outboundMsg); err != nil {
			return fmt.Errorf("save outbound telegram message: %w", err)
		}

		// 2. Mark delivery sent
		if err := w.repo.MarkDeliverySent(ctx, delivery.ID, sentMsg.MessageID); err != nil {
			w.logger().Error("failed to mark delivery sent", slog.String("delivery_id", delivery.ID), slog.String("error", err.Error()))
			return err
		}
	}

	w.logger().Info("telegram delivery sent successfully",
		slog.String("delivery_id", delivery.ID),
		slog.Int64("chat_id", delivery.ChatID),
		slog.Int64("message_id", sentMsg.MessageID),
	)
	return nil
}

func (w *DeliveryWorker) failDelivery(ctx context.Context, delivery *TelegramDelivery, cause error) error {
	w.logger().Warn("telegram reply generation failed", slog.String("delivery_id", delivery.ID), slog.String("error", cause.Error()))
	if w.repo != nil {
		if err := w.repo.MarkDeliveryFailed(ctx, delivery.ID, cause.Error()); err != nil {
			return fmt.Errorf("%w; mark delivery failed: %v", cause, err)
		}
	}
	return cause
}

func (w *DeliveryWorker) logger() *slog.Logger {
	if w.log != nil {
		return w.log
	}
	return slog.Default()
}

// ProcessPendingBatch finds and delivers a batch of pending deliveries.
func (w *DeliveryWorker) ProcessPendingBatch(ctx context.Context, limit int) (int, error) {
	deliveries, err := w.repo.ListPendingDeliveries(ctx, limit, w.maxAttempts, w.lockTimeout)
	if err != nil {
		return 0, err
	}

	successCount := 0
	for _, d := range deliveries {
		claimed, err := w.repo.ClaimPendingDelivery(ctx, d.ID, w.workerID, w.maxAttempts, w.lockTimeout)
		if err != nil {
			continue // Lost race to another worker
		}

		if err := w.ProcessDelivery(ctx, claimed); err == nil {
			successCount++
		}
	}
	return successCount, nil
}

// Start runs the delivery worker in a background loop.
func (w *DeliveryWorker) Start(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	w.log.Info("telegram delivery worker started", slog.String("worker_id", w.workerID))

	for {
		select {
		case <-ctx.Done():
			w.log.Info("telegram delivery worker stopping")
			return
		case <-ticker.C:
			_, _ = w.ProcessPendingBatch(ctx, 20)
		}
	}
}
