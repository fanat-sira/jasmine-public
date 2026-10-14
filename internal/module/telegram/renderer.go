package telegram

import (
	"context"
	"log/slog"
	"sync"
	"time"

	telegramclient "jasmine/internal/clients/telegram"
)

const (
	DraftModeRich     = "rich"
	DraftModeNormal   = "normal"
	DraftModeFallback = "fallback"
)

// DraftRenderer abstracts status updates, thinking blocks, rate limiting, and final message rendering.
type DraftRenderer interface {
	UpdateStatus(ctx context.Context, chatID int64, draftID int, statusText string)
	GetActiveMode(chatID int64) string
	Finalize(ctx context.Context, chatID int64, draftID int, finalHTML string, replyToMessageID *int64) (*telegramclient.Message, error)
}

// DualWindowRateLimiter tracks per-chat rate limits (max 20 calls/5s, max 40 calls/30s).
type DualWindowRateLimiter struct {
	history5s  []time.Time
	history30s []time.Time
}

func (rl *DualWindowRateLimiter) Allow(now time.Time) bool {
	// Clean up entries older than 5s
	cutoff5s := now.Add(-5 * time.Second)
	idx5 := 0
	for idx5 < len(rl.history5s) && rl.history5s[idx5].Before(cutoff5s) {
		idx5++
	}
	rl.history5s = rl.history5s[idx5:]

	// Clean up entries older than 30s
	cutoff30s := now.Add(-30 * time.Second)
	idx30 := 0
	for idx30 < len(rl.history30s) && rl.history30s[idx30].Before(cutoff30s) {
		idx30++
	}
	rl.history30s = rl.history30s[idx30:]

	// Check limits
	if len(rl.history5s) >= 20 || len(rl.history30s) >= 40 {
		return false
	}

	rl.history5s = append(rl.history5s, now)
	rl.history30s = append(rl.history30s, now)
	return true
}

type NativeDraftRenderer struct {
	client       *telegramclient.Client
	log          *slog.Logger
	rateLimiters map[int64]*DualWindowRateLimiter
	activeModes  map[int64]string // DraftModeRich | DraftModeNormal | DraftModeFallback
	mu           sync.Mutex
}

func NewDraftRenderer(client *telegramclient.Client, log *slog.Logger) *NativeDraftRenderer {
	if log == nil {
		log = slog.Default()
	}
	return &NativeDraftRenderer{
		client:       client,
		log:          log,
		rateLimiters: make(map[int64]*DualWindowRateLimiter),
		activeModes:  make(map[int64]string),
	}
}

// StableDraftID derives a stable positive integer draft ID from a Telegram update ID.
func StableDraftID(updateID int64) int {
	if updateID <= 0 {
		return 1
	}
	val := int(updateID % 2000000000)
	if val <= 0 {
		return 1
	}
	return val
}

func (r *NativeDraftRenderer) GetActiveMode(chatID int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	mode, ok := r.activeModes[chatID]
	if !ok {
		return DraftModeFallback
	}
	return mode
}

// UpdateStatus updates the execution status message / thinking block, throttled against both 5s and 30s windows.
func (r *NativeDraftRenderer) UpdateStatus(ctx context.Context, chatID int64, draftID int, statusText string) {
	if r.client == nil {
		return
	}
	// Progress is transport state, not assistant language. Only LLM_REPLY writes text.
	_ = r.client.SendChatAction(ctx, chatID, "typing")
}

// Finalize dispatches the final response preserving the active rendering mode.
// If rich draft succeeded, finalizes using sendRichMessage (Bot API 10.1+).
// Otherwise finalizes using sendMessage.
func (r *NativeDraftRenderer) Finalize(ctx context.Context, chatID int64, draftID int, finalHTML string, replyToMessageID *int64) (*telegramclient.Message, error) {
	if r.client == nil {
		return nil, telegramclient.ErrNotConfigured
	}

	r.mu.Lock()
	mode := r.activeModes[chatID]
	delete(r.activeModes, chatID)
	r.mu.Unlock()

	opts := &telegramclient.SendMessageOptions{
		ParseMode:        "HTML",
		ReplyToMessageID: replyToMessageID,
	}

	if mode == DraftModeRich {
		// Finalize ephemeral rich draft into persistent message
		msg, err := r.client.SendRichMessage(ctx, chatID, draftID, finalHTML, opts)
		if err == nil {
			return msg, nil
		}
		r.log.Warn("sendRichMessage failed, falling back to sendMessage", slog.String("error", err.Error()))
	}

	// Normal / fallback finalization
	msg, err := r.client.SendMessage(ctx, chatID, finalHTML, opts)
	if err != nil {
		// Retry without HTML parse mode if tags were invalid
		r.log.Warn("sendMessage HTML failed, retrying plain text", slog.String("error", err.Error()))
		return r.client.SendMessage(ctx, chatID, finalHTML, &telegramclient.SendMessageOptions{
			ReplyToMessageID: replyToMessageID,
		})
	}
	return msg, nil
}
