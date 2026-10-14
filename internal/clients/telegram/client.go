package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrNotConfigured   = errors.New("telegram bot token is not configured")
	ErrTelegramAPI     = errors.New("telegram api error")
	ErrFeatureDisabled = errors.New("telegram feature not supported")
)

type Client struct {
	botToken   string
	apiBaseURL string
	httpClient *http.Client
}

func NewClient(botToken string) *Client {
	return &Client{
		botToken:   strings.TrimSpace(botToken),
		apiBaseURL: "https://api.telegram.org",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewClientWithBaseURL allows custom base URLs (useful for unit testing / mocking).
func NewClientWithBaseURL(botToken, apiBaseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		botToken:   strings.TrimSpace(botToken),
		apiBaseURL: strings.TrimRight(apiBaseURL, "/"),
		httpClient: httpClient,
	}
}

// API Models

type User struct {
	ID           int64   `json:"id"`
	IsBot        bool    `json:"is_bot"`
	FirstName    string  `json:"first_name"`
	LastName     *string `json:"last_name,omitempty"`
	Username     *string `json:"username,omitempty"`
	LanguageCode *string `json:"language_code,omitempty"`
}

type Chat struct {
	ID        int64   `json:"id"`
	Type      string  `json:"type"` // "private", "group", "supergroup", "channel"
	Title     *string `json:"title,omitempty"`
	Username  *string `json:"username,omitempty"`
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
}

type Message struct {
	MessageID    int64       `json:"message_id"`
	From         *User       `json:"from,omitempty"`
	Chat         Chat        `json:"chat"`
	Date         int64       `json:"date"`
	Text         *string     `json:"text,omitempty"`
	Caption      *string     `json:"caption,omitempty"`
	Photo        []PhotoSize `json:"photo,omitempty"`
	Document     *Document   `json:"document,omitempty"`
	MediaGroupID string      `json:"media_group_id,omitempty"`
	ReplyTo      *Message    `json:"reply_to_message,omitempty"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// PhotoSize is a Telegram-provided rendition. Only the largest rendition is processed.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

func BestPhotoWithin(photos []PhotoSize, maxBytes int64) *PhotoSize {
	var best *PhotoSize
	for i := range photos {
		p := &photos[i]
		if p.FileID == "" || (maxBytes > 0 && p.FileSize > maxBytes) {
			continue
		}
		if best == nil || p.Width*p.Height > best.Width*best.Height || (p.Width*p.Height == best.Width*best.Height && p.FileSize > best.FileSize) {
			best = p
		}
	}
	return best
}

func BestPhoto(photos []PhotoSize) *PhotoSize {
	var best *PhotoSize
	for i := range photos {
		p := &photos[i]
		if p.FileID != "" && (best == nil || p.Width*p.Height > best.Width*best.Height || (p.Width*p.Height == best.Width*best.Height && p.FileSize > best.FileSize)) {
			best = p
		}
	}
	return best
}

type Update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *Message `json:"message,omitempty"`
	EditedMessage *Message `json:"edited_message,omitempty"`
}

type SendMessageOptions struct {
	ParseMode        string `json:"parse_mode,omitempty"` // "HTML", "MarkdownV2"
	ReplyToMessageID *int64 `json:"reply_to_message_id,omitempty"`
}

type ResponseParameters struct {
	MigrateToChatID *int64 `json:"migrate_to_chat_id,omitempty"`
	RetryAfter      *int   `json:"retry_after,omitempty"`
}

type ApiResponse[T any] struct {
	OK          bool                `json:"ok"`
	Result      T                   `json:"result"`
	ErrorCode   *int                `json:"error_code,omitempty"`
	Description *string             `json:"description,omitempty"`
	Parameters  *ResponseParameters `json:"parameters,omitempty"`
}

type RichBlockThinking struct {
	Type string `json:"type"` // "thinking"
	Text string `json:"text"`
}

// SendMessage delivers a persistent message to a Telegram chat.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, opts *SendMessageOptions) (*Message, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}

	payload := map[string]any{
		"chat_id": chatID,
		"text":    NormalizeBotText(text),
	}
	if opts != nil {
		if opts.ParseMode != "" {
			payload["parse_mode"] = opts.ParseMode
		}
		if opts.ReplyToMessageID != nil {
			payload["reply_to_message_id"] = *opts.ReplyToMessageID
		}
	}

	var resp ApiResponse[Message]
	if err := c.post(ctx, "sendMessage", payload, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return nil, fmt.Errorf("%w: %s (code %v)", ErrTelegramAPI, desc, resp.ErrorCode)
	}
	return &resp.Result, nil
}

// GetFile resolves a file ID without returning a token-bearing URL.
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}
	var resp ApiResponse[File]
	if err := c.post(ctx, "getFile", map[string]any{"file_id": fileID}, &resp); err != nil {
		return nil, err
	}
	if !resp.OK || resp.Result.FilePath == "" {
		return nil, fmt.Errorf("%w: getFile failed", ErrTelegramAPI)
	}
	return &resp.Result, nil
}

// DownloadFile returns bounded transient bytes; callers must not log them.
func (c *Client) DownloadFile(ctx context.Context, file *File, maxBytes int64) ([]byte, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}
	if file == nil || file.FilePath == "" {
		return nil, fmt.Errorf("telegram file path is required")
	}
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	if file.FileSize > maxBytes {
		return nil, fmt.Errorf("telegram file exceeds %d byte limit", maxBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/file/bot%s/%s", c.apiBaseURL, c.botToken, strings.TrimLeft(file.FilePath, "/")), nil)
	if err != nil {
		return nil, fmt.Errorf("build Telegram file request: %s", sanitizeTelegramError(err, c.botToken))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download Telegram file: %s", sanitizeTelegramError(err, c.botToken))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: download status %d", ErrTelegramAPI, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("telegram file exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

// SendRichMessage finalizes an ephemeral rich draft into a persistent message (Bot API 10.1+).
func (c *Client) SendRichMessage(ctx context.Context, chatID int64, draftID int, text string, opts *SendMessageOptions) (*Message, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}

	payload := map[string]any{
		"chat_id":  chatID,
		"draft_id": draftID,
		"text":     NormalizeBotText(text),
	}
	if opts != nil {
		if opts.ParseMode != "" {
			payload["parse_mode"] = opts.ParseMode
		}
		if opts.ReplyToMessageID != nil {
			payload["reply_to_message_id"] = *opts.ReplyToMessageID
		}
	}

	var resp ApiResponse[Message]
	if err := c.post(ctx, "sendRichMessage", payload, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return nil, fmt.Errorf("%w: %s (code %v)", ErrTelegramAPI, desc, resp.ErrorCode)
	}
	return &resp.Result, nil
}

// SendChatAction sends a status action (e.g. "typing").
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	if c.botToken == "" {
		return ErrNotConfigured
	}
	payload := map[string]any{
		"chat_id": chatID,
		"action":  action,
	}
	var resp ApiResponse[bool]
	if err := c.post(ctx, "sendChatAction", payload, &resp); err != nil {
		return err
	}
	return nil
}

// SendMessageDraft sends or updates a message draft.
func (c *Client) SendMessageDraft(ctx context.Context, chatID int64, draftID int, text string) error {
	if c.botToken == "" {
		return ErrNotConfigured
	}
	payload := map[string]any{
		"chat_id":  chatID,
		"draft_id": draftID,
		"text":     NormalizeBotText(text),
	}
	var resp ApiResponse[bool]
	if err := c.post(ctx, "sendMessageDraft", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return nil
}

// SendRichMessageDraft sends or updates a rich message draft containing thinking blocks.
func (c *Client) SendRichMessageDraft(ctx context.Context, chatID int64, draftID int, blocks []RichBlockThinking) error {
	if c.botToken == "" {
		return ErrNotConfigured
	}
	payload := map[string]any{
		"chat_id":  chatID,
		"draft_id": draftID,
		"blocks":   normalizeThinkingBlocks(blocks),
	}
	var resp ApiResponse[bool]
	if err := c.post(ctx, "sendRichMessageDraft", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return nil
}

// DeleteWebhook switches the bot away from webhook delivery without dropping queued updates.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	if c.botToken == "" {
		return ErrNotConfigured
	}
	var resp ApiResponse[bool]
	if err := c.post(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, &resp); err != nil {
		return err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return nil
}

// GetUpdatesRaw long-polls Telegram while preserving the original update JSON.
func (c *Client) GetUpdatesRaw(ctx context.Context, offset int64) ([]json.RawMessage, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}
	payload := map[string]any{"offset": offset, "limit": 100, "timeout": 20, "allowed_updates": []string{"message", "edited_message"}}
	var resp ApiResponse[[]json.RawMessage]
	if err := c.post(ctx, "getUpdates", payload, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return nil, fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return resp.Result, nil
}

// SetWebhook registers the webhook URL and secret token with Telegram.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secretToken string) error {
	if c.botToken == "" {
		return ErrNotConfigured
	}
	payload := map[string]any{
		"url": webhookURL,
	}
	if secretToken != "" {
		payload["secret_token"] = secretToken
	}
	var resp ApiResponse[bool]
	if err := c.post(ctx, "setWebhook", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return nil
}

type WebhookInfo struct {
	URL                  string  `json:"url"`
	HasCustomCertificate bool    `json:"has_custom_certificate"`
	PendingUpdateCount   int     `json:"pending_update_count"`
	LastErrorDate        *int64  `json:"last_error_date,omitempty"`
	LastErrorMessage     *string `json:"last_error_message,omitempty"`
}

// GetWebhookInfo retrieves the current webhook status from Telegram.
func (c *Client) GetWebhookInfo(ctx context.Context) (*WebhookInfo, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}
	var resp ApiResponse[WebhookInfo]
	if err := c.post(ctx, "getWebhookInfo", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return nil, fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return &resp.Result, nil
}

// GetMe fetches the bot's own profile.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	if c.botToken == "" {
		return nil, ErrNotConfigured
	}
	var resp ApiResponse[User]
	if err := c.post(ctx, "getMe", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		desc := "unknown error"
		if resp.Description != nil {
			desc = *resp.Description
		}
		return nil, fmt.Errorf("%w: %s", ErrTelegramAPI, desc)
	}
	return &resp.Result, nil
}

func (c *Client) post(ctx context.Context, method string, bodyPayload any, target any) error {
	url := fmt.Sprintf("%s/bot%s/%s", c.apiBaseURL, c.botToken, method)
	var bodyReader io.Reader
	if bodyPayload != nil {
		raw, err := json.Marshal(bodyPayload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return fmt.Errorf("build Telegram request: %s", sanitizeTelegramError(err, c.botToken))
	}
	if bodyPayload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram request: %s", sanitizeTelegramError(err, c.botToken))
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if target != nil {
		if err := json.Unmarshal(respBytes, target); err != nil {
			return fmt.Errorf("unmarshal response: %w (raw: %s)", err, string(respBytes))
		}
	}
	return nil
}

func sanitizeTelegramError(err error, token string) string {
	if err == nil {
		return ""
	}
	if token == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), token, "[redacted]")
}

func normalizeThinkingBlocks(blocks []RichBlockThinking) []RichBlockThinking {
	out := make([]RichBlockThinking, len(blocks))
	for i, block := range blocks {
		out[i] = block
		out[i].Text = NormalizeBotText(block.Text)
	}
	return out
}
