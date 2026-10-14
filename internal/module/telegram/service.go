package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	telegramclient "jasmine/internal/clients/telegram"
	"jasmine/internal/pkg/correlation"
	"jasmine/internal/pkg/identity"
)

type IngestionHandler interface {
	IngestMessage(ctx context.Context, userID string, chatID int64, updateID int64, messageID int64, replyToMessageID *int64, rawText string) (*IngestionResponse, error)
}

type ImageIngestionHandler interface {
	IngestImage(ctx context.Context, userID string, chatID, updateID, messageID int64, replyToMessageID *int64, caption string, image ImageInput) (*IngestionResponse, error)
}
type ImageInput struct {
	FileID       string
	FileUniqueID string
	MIMEType     string
	Bytes        []byte
}

type IngestionResponse struct {
	Success          bool
	WorkoutID        string
	NeedsClarify     bool
	ClarificationMsg string
	ConfirmationHTML string
	ReplyContext     *ReplyContext
	DomainReferences []DomainReference
}

type Service struct {
	repo                      *Repository
	client                    *telegramclient.Client
	renderer                  DraftRenderer
	ingestionPipeline         IngestionHandler
	imageIngestionPipeline    ImageIngestionHandler
	albumIngestionPipeline    AlbumIngestionHandler
	documentIngestionPipeline DocumentIngestionHandler
	deliveryWorker            *DeliveryWorker
	log                       *slog.Logger
	workerID                  string
	maxAttempts               int
	lockTimeout               time.Duration
	allowedUserIDs            map[int64]struct{}
}

func NewService(repo *Repository, client *telegramclient.Client, renderer DraftRenderer, worker *DeliveryWorker, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
	return &Service{
		repo:           repo,
		client:         client,
		renderer:       renderer,
		deliveryWorker: worker,
		log:            log,
		workerID:       workerID,
		maxAttempts:    3,
		lockTimeout:    60 * time.Second,
	}
}

func (s *Service) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// SetAllowedUserIDs restricts processing to these Telegram sender IDs.
// An empty list denies all senders.
func (s *Service) SetAllowedUserIDs(ids []int64) {
	if len(ids) == 0 {
		s.allowedUserIDs = nil
		return
	}
	allowed := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	s.allowedUserIDs = allowed
}

func (s *Service) SetIngestionPipeline(pipeline IngestionHandler) { s.ingestionPipeline = pipeline }
func (s *Service) SetImageIngestionPipeline(pipeline ImageIngestionHandler) {
	s.imageIngestionPipeline = pipeline
}
func (s *Service) SetAlbumIngestionPipeline(pipeline AlbumIngestionHandler) {
	s.albumIngestionPipeline = pipeline
}
func (s *Service) SetDocumentIngestionPipeline(pipeline DocumentIngestionHandler) {
	s.documentIngestionPipeline = pipeline
}

// ProcessWebhookUpdate orchestrates ingress, idempotency, user provisioning, inbound persistence, and outbox creation.
func (s *Service) ProcessWebhookUpdate(ctx context.Context, updateRaw []byte) error {
	var update telegramclient.Update
	if err := json.Unmarshal(updateRaw, &update); err != nil {
		return fmt.Errorf("unmarshal telegram update: %w", err)
	}

	if update.UpdateID == 0 {
		return fmt.Errorf("invalid update: missing update_id")
	}

	msg := update.Message
	if msg == nil {
		msg = update.EditedMessage
	}
	if msg == nil {
		s.logger().Debug("ignoring non-message update", slog.Int64("update_id", update.UpdateID))
		return nil
	}

	chatID := msg.Chat.ID
	fromUser := msg.From
	var telegramUserID int64
	if fromUser != nil {
		telegramUserID = fromUser.ID
	} else {
		telegramUserID = chatID
	}

	// 1. Authorize the immutable sender ID before any persistence or model work.
	_, allowed := s.allowedUserIDs[telegramUserID]
	if fromUser == nil || !allowed {
		s.logger().Info("ignoring telegram sender outside allowlist", slog.Int64("update_id", update.UpdateID))
		return nil
	}
	if msg.Chat.Type != ChatTypePrivate {
		s.logger().Info("ignoring non-private chat", slog.Int64("update_id", update.UpdateID))
		return nil
	}
	corrID := correlation.GenerateUpdateCorrelationID(update.UpdateID)
	ctx = correlation.WithCorrelationID(ctx, corrID)

	// 2. Register update in idempotency ledger
	if s.repo != nil {
		rawUpdate := &TelegramUpdate{
			UpdateID:       update.UpdateID,
			CorrelationID:  corrID,
			ChatID:         chatID,
			TelegramUserID: telegramUserID,
			Status:         UpdateStatusReceived,
			Attempts:       0,
			RawPayload:     updateRaw,
			CreatedAt:      time.Now(),
		}
		if err := s.repo.SaveUpdate(ctx, rawUpdate); err != nil {
			return fmt.Errorf("save update to ledger: %w", err)
		}
	}

	// 3. Atomically claim update
	var claimed *TelegramUpdate
	if s.repo != nil {
		var err error
		claimed, err = s.repo.ClaimUpdate(ctx, update.UpdateID, s.workerID, s.maxAttempts, s.lockTimeout)
		if err != nil {
			if errors.Is(err, ErrUpdateConflict) {
				s.logger().Info("update already processed or actively claimed by another worker",
					slog.Int64("update_id", update.UpdateID),
				)
				return nil
			}
			return fmt.Errorf("claim update %d: %w", update.UpdateID, err)
		}
		if claimed.CorrelationID != "" {
			corrID = claimed.CorrelationID
			ctx = correlation.WithCorrelationID(ctx, corrID)
		}
	} else {
		claimed = &TelegramUpdate{
			UpdateID:       update.UpdateID,
			CorrelationID:  corrID,
			ChatID:         chatID,
			TelegramUserID: telegramUserID,
			Status:         UpdateStatusProcessing,
		}
	}

	// 4. Provision / Retrieve Telegram User Atomically
	var langCode string
	if fromUser != nil && fromUser.LanguageCode != nil {
		langCode = *fromUser.LanguageCode
	}
	var firstName, lastName, username *string
	if fromUser != nil {
		firstName = &fromUser.FirstName
		lastName = fromUser.LastName
		username = fromUser.Username
	}

	var tgUser *TelegramUser
	if s.repo != nil {
		var err error
		tgUser, err = s.repo.FindOrCreateTelegramUser(ctx, &TelegramUser{
			TelegramUserID: telegramUserID,
			Username:       username,
			FirstName:      firstName,
			LastName:       lastName,
			LanguageCode:   langCode,
		})
		if err != nil {
			_ = s.repo.FailUpdate(ctx, claimed.UpdateID, fmt.Sprintf("find or create user: %v", err))
			return fmt.Errorf("find or create user: %w", err)
		}
	} else {
		tgUser = &TelegramUser{
			TelegramUserID: telegramUserID,
			UserID:         fmt.Sprintf("user-%d", telegramUserID),
			Username:       username,
		}
	}

	ctx = identity.WithUserID(ctx, tgUser.UserID)

	// 5. Persist Inbound Message
	var replyToID *int64
	if msg.ReplyTo != nil {
		replyToID = &msg.ReplyTo.MessageID
	}
	rawMsgJSON, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal inbound telegram message: %w", err)
	}
	inboundMsg := &TelegramMessage{
		ChatID:           chatID,
		MessageID:        msg.MessageID,
		CorrelationID:    corrID,
		TelegramUserID:   &telegramUserID,
		UserID:           tgUser.UserID,
		Direction:        DirectionInbound,
		ReplyToMessageID: replyToID,
		Text: func() *string {
			if msg.Text != nil {
				return msg.Text
			}
			return msg.Caption
		}(),
		RawPayload:       rawMsgJSON,
		DomainReferences: []DomainReference{},
		CreatedAt:        time.Unix(msg.Date, 0),
	}
	if s.repo != nil {
		if err := s.repo.SaveMessage(ctx, inboundMsg); err != nil {
			if failErr := s.repo.FailUpdate(ctx, claimed.UpdateID, fmt.Sprintf("save inbound message: %v", err)); failErr != nil {
				return fmt.Errorf("save inbound message: %w; mark update failed: %v", err, failErr)
			}
			return fmt.Errorf("save inbound message: %w", err)
		}
	}

	// 6. Handle Message / Ingestion Pipeline Dispatch
	text := ""
	if msg.Text != nil {
		text = strings.TrimSpace(*msg.Text)
	}
	if text == "" && msg.Caption != nil {
		text = strings.TrimSpace(*msg.Caption)
	}

	var replyFacts *ReplyContext
	var domainRefs []DomainReference
	var document *DocumentInput
	if msg.Document != nil {
		document = &DocumentInput{Kind: ClassifyDocument(msg.Document.FileName, msg.Document.MimeType), Filename: msg.Document.FileName, MIMEType: msg.Document.MimeType, FileID: msg.Document.FileID, FileUniqueID: msg.Document.FileUniqueID, MessageID: msg.MessageID, Caption: text}
		if document.Kind == DocumentText || document.Kind == DocumentJSON {
			limit := int64(256 << 10)
			if document.Kind == DocumentJSON {
				limit = 64 << 10
			}
			if s.client == nil || msg.Document.FileSize > limit {
				replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"document_size_limit"}, CurrentState: map[string]any{"max_bytes": limit}}
			} else if file, err := s.client.GetFile(ctx, msg.Document.FileID); err != nil {
				replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"document_file_unavailable"}}
			} else if data, err := s.client.DownloadFile(ctx, file, limit); err != nil {
				replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"document_download_failed"}}
			} else if !utf8.Valid(data) {
				replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"document_utf8_required"}}
			} else {
				document.Text = string(data)
			}
		}
	}
	if replyFacts != nil {
		// Document read error is returned through the normal outbox below.
	} else if document != nil && document.Kind == DocumentPhoto {
		if s.imageIngestionPipeline == nil || s.client == nil || msg.Document.FileSize > 10<<20 {
			replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_unavailable"}}
		} else if file, err := s.client.GetFile(ctx, document.FileID); err != nil {
			replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_file_unavailable"}}
		} else if data, err := s.client.DownloadFile(ctx, file, 10<<20); err != nil {
			replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_download_failed"}}
		} else {
			mime := http.DetectContentType(data)
			if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" && mime != "image/gif" {
				replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_invalid_format"}}
			} else {
				res, err := s.imageIngestionPipeline.IngestImage(ctx, tgUser.UserID, chatID, claimed.UpdateID, msg.MessageID, replyToID, text, ImageInput{FileID: document.FileID, FileUniqueID: document.FileUniqueID, MIMEType: mime, Bytes: data})
				if err != nil {
					_ = s.repo.FailUpdate(ctx, claimed.UpdateID, err.Error())
					return fmt.Errorf("ingest image document: %w", err)
				}
				if res != nil && res.Success {
					return nil
				}
				replyFacts = replyContextForIngestionResponse(res)
			}
		}
	} else if document != nil {
		if s.documentIngestionPipeline == nil {
			replyFacts = &ReplyContext{EventType: "document_handler_unavailable", CurrentState: map[string]any{"filename": document.Filename, "kind": document.Kind}}
		} else {
			res, err := s.documentIngestionPipeline.IngestDocument(ctx, tgUser.UserID, chatID, claimed.UpdateID, msg.MessageID, replyToID, *document)
			if err != nil {
				_ = s.repo.FailUpdate(ctx, claimed.UpdateID, err.Error())
				return fmt.Errorf("ingest document: %w", err)
			}
			if res != nil && res.Success {
				if err := s.repo.CompleteUpdate(ctx, claimed.UpdateID, []byte(`{"action":"document_processed"}`)); err != nil {
					return err
				}
				return nil
			}
			replyFacts = replyContextForIngestionResponse(res)
		}
	} else if msg.MediaGroupID != "" && len(msg.Photo) > 0 {
		photo := telegramclient.BestPhotoWithin(msg.Photo, 10<<20)
		if photo == nil {
			replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_unavailable"}}
		} else if s.repo == nil {
			return fmt.Errorf("media group requires durable repository")
		} else {
			if err := s.repo.AddMediaGroupItem(ctx, tgUser.UserID, chatID, msg.MediaGroupID, claimed.UpdateID, msg.MessageID, photo.FileID, photo.FileUniqueID, text); err != nil {
				_ = s.repo.FailUpdate(ctx, claimed.UpdateID, err.Error())
				return fmt.Errorf("collect media group item: %w", err)
			}
			return nil
		}
	} else if len(msg.Photo) > 0 && s.imageIngestionPipeline != nil && s.client != nil {
		photo := telegramclient.BestPhotoWithin(msg.Photo, 10<<20)
		if photo == nil {
			replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_unavailable"}}
		} else {
			f, err := s.client.GetFile(ctx, photo.FileID)
			if err == nil {
				var b []byte
				b, err = s.client.DownloadFile(ctx, f, 10<<20)
				if err == nil {
					mime := http.DetectContentType(b)
					if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" && mime != "image/gif" {
						err = fmt.Errorf("unsupported image type")
					}
				}
				if err == nil {
					var res *IngestionResponse
					res, err = s.imageIngestionPipeline.IngestImage(ctx, tgUser.UserID, chatID, claimed.UpdateID, msg.MessageID, replyToID, text, ImageInput{FileID: photo.FileID, FileUniqueID: photo.FileUniqueID, MIMEType: http.DetectContentType(b), Bytes: b})
					if res != nil {
						if res.Success {
							return nil
						}
						replyFacts = replyContextForIngestionResponse(res)
						domainRefs = res.DomainReferences
					}
				}
				b = nil
			}
			if err != nil {
				replyFacts = &ReplyContext{EventType: "attachment_error", Errors: []string{"photo_analysis_failed"}}
			}
		}
	} else if text == "/start" || text == "/help" {
		replyFacts = &ReplyContext{EventType: "greeting"}
	} else if s.ingestionPipeline != nil && text != "" {
		ingestRes, err := s.ingestionPipeline.IngestMessage(ctx, tgUser.UserID, chatID, claimed.UpdateID, msg.MessageID, replyToID, text)
		if err != nil {
			if s.repo != nil {
				if failErr := s.repo.FailUpdate(ctx, claimed.UpdateID, fmt.Sprintf("ingest message: %v", err)); failErr != nil {
					return fmt.Errorf("ingest message: %w; mark update failed: %v", err, failErr)
				}
			}
			return fmt.Errorf("ingest message: %w", err)
		}
		if ingestRes != nil {
			if ingestRes.NeedsClarify {
				replyFacts = replyContextForIngestionResponse(ingestRes)
			} else if ingestRes.Success {
				if s.repo != nil {
					respSummary, err := json.Marshal(map[string]any{
						"workout_id": ingestRes.WorkoutID,
						"status":     "persisted",
					})
					if err != nil {
						return fmt.Errorf("marshal ingestion response: %w", err)
					}
					if err := s.repo.CompleteUpdate(ctx, claimed.UpdateID, respSummary); err != nil {
						return fmt.Errorf("complete ingested update: %w", err)
					}
				}
				return nil
			} else if ingestRes.ConfirmationHTML != "" || ingestRes.ReplyContext != nil {
				replyFacts = replyContextForIngestionResponse(ingestRes)
			}
			domainRefs = ingestRes.DomainReferences
		}
	} else if text != "" {
		replyFacts = &ReplyContext{EventType: "general_chat"}
	} else {
		replyFacts = &ReplyContext{EventType: "unsupported_input", Clarification: []string{"text_or_single_photo_needed"}}
	}

	// 7. Atomically enqueue durable delivery outbox
	draftID := StableDraftID(claimed.UpdateID)
	isRich := s.renderer != nil && s.renderer.GetActiveMode(chatID) == DraftModeRich

	outboxPayload, err := json.Marshal(OutboxPayload{
		DraftID:          &draftID,
		IsRich:           isRich,
		Text:             "",
		ReplyContext:     replyFacts,
		ParseMode:        "HTML",
		ReplyToMessageID: &msg.MessageID,
		DomainReferences: domainRefs,
	})
	if err != nil {
		return fmt.Errorf("marshal response delivery: %w", err)
	}

	delivery := &TelegramDelivery{
		CorrelationID: corrID,
		UpdateID:      &claimed.UpdateID,
		ChatID:        chatID,
		UserID:        tgUser.UserID,
		Status:        DeliveryStatusPending,
		Payload:       outboxPayload,
		Attempts:      0,
	}
	if s.repo != nil {
		if err := s.repo.CreateDelivery(ctx, delivery); err != nil {
			_ = s.repo.FailUpdate(ctx, claimed.UpdateID, fmt.Sprintf("create delivery: %v", err))
			return fmt.Errorf("create delivery: %w", err)
		}

		// 8. Mark update completed
		respSummary, err := json.Marshal(map[string]any{
			"delivery_id": delivery.ID,
			"event_type":  replyFacts.EventType,
		})
		if err != nil {
			return fmt.Errorf("marshal response summary: %w", err)
		}
		if err := s.repo.CompleteUpdate(ctx, claimed.UpdateID, respSummary); err != nil {
			return fmt.Errorf("complete response update: %w", err)
		}
	}

	return nil
}

// replyContextForIngestionResponse carries action status and references to
// LLM_REPLY without forwarding a legacy prewritten Telegram sentence.
func replyContextForIngestionResponse(res *IngestionResponse) *ReplyContext {
	if res == nil {
		return &ReplyContext{EventType: "pipeline_no_result"}
	}
	if res.ReplyContext != nil {
		facts := *res.ReplyContext
		if len(facts.ToolResults) == 0 {
			facts.ToolResults = res.DomainReferences
		}
		return &facts
	}
	if res.NeedsClarify {
		return &ReplyContext{EventType: "clarification_required", Clarification: []string{"missing_information"}, ToolResults: res.DomainReferences}
	}
	return &ReplyContext{EventType: "pipeline_result", ToolResults: res.DomainReferences}
}
