package openrouter

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

	"jasmine/internal/pkg/correlation"
	"jasmine/internal/pkg/identity"
)

const apiURL = "https://openrouter.ai/api/v1/chat/completions"

var ErrNotConfigured = errors.New("openrouter: API key not set")

type LLMCallRecord struct {
	CorrelationID         string
	UserID                string
	Operation             string
	Provider              string
	Model                 string
	LatencyMs             int64
	Success               bool
	ErrorMessage          *string
	RetryCount            int
	InputTokens           *int
	OutputTokens          *int
	StructuredOutputValid bool
	CreatedAt             time.Time
}

type LLMObserver interface {
	RecordLLMCall(ctx context.Context, record LLMCallRecord)
}

type Client struct {
	apiKey   string
	model    string
	apiURL   string
	http     *http.Client
	observer LLMObserver
}

func NewClient(apiKey, model string) *Client {
	return &Client{
		apiKey: apiKey,
		model:  model,
		apiURL: apiURL,
		http:   &http.Client{Timeout: 90 * time.Second},
	}
}

func NewClientWithBaseURL(apiKey, model, customURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	return &Client{
		apiKey: apiKey,
		model:  model,
		apiURL: customURL,
		http:   httpClient,
	}
}

func (c *Client) SetObserver(observer LLMObserver) {
	c.observer = observer
}

func (c *Client) GetObserver() LLMObserver {
	return c.observer
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatRequest struct {
	Model     string    `json:"model"`
	SessionID string    `json:"session_id,omitempty"`
	Messages  []Message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete sends a single text message using the client's default model.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	return c.SendChat(ctx, "", prompt)
}

// RecordStructuredValidation allows callers (extractors, generators) to log whether structured JSON parsing succeeded.
func (c *Client) RecordStructuredValidation(
	ctx context.Context,
	operation string,
	model string,
	latencyMs int64,
	valid bool,
	validationErr error,
) {
	if c.observer == nil {
		return
	}
	corrID := correlation.GetCorrelationID(ctx)
	if corrID == "" {
		corrID = correlation.GenerateID("llm")
	}
	userID := identity.GetUserID(ctx)
	if model == "" {
		model = c.model
	}

	var errMsg *string
	if validationErr != nil {
		s := validationErr.Error()
		errMsg = &s
	}

	c.observer.RecordLLMCall(ctx, LLMCallRecord{
		CorrelationID:         corrID,
		UserID:                userID,
		Operation:             operation,
		Provider:              "openrouter",
		Model:                 model,
		LatencyMs:             latencyMs,
		Success:               true, // provider returned 200
		ErrorMessage:          errMsg,
		RetryCount:            correlation.GetAttempt(ctx),
		StructuredOutputValid: valid,
		CreatedAt:             time.Now(),
	})
}

// SendChat sends a single user message. Content can be either a plain string
// or a []ContentPart for multimodal requests.
func (c *Client) SendChat(ctx context.Context, model string, content any) (string, error) {
	return c.sendChat(ctx, model, "", nil, content)
}

// SendChatWithSystem sends a system instruction followed by the user content.
func (c *Client) SendChatWithSystem(ctx context.Context, model, systemPrompt string, content any) (string, error) {
	return c.sendChat(ctx, model, systemPrompt, nil, content)
}

// SendConversation sends system instructions, ordered prior turns, and the current user turn.
func (c *Client) SendConversation(ctx context.Context, model, systemPrompt string, history []Message, content any) (string, error) {
	return c.sendChat(ctx, model, systemPrompt, history, content)
}

func (c *Client) sendChat(ctx context.Context, model, systemPrompt string, history []Message, content any) (string, error) {
	start := time.Now()
	corrID := correlation.GetCorrelationID(ctx)
	if corrID == "" {
		corrID = correlation.GenerateID("llm")
	}
	userID := identity.GetUserID(ctx)
	operation := correlation.GetOperation(ctx)
	if operation == "" {
		operation = "llm_chat"
	}
	attempt := correlation.GetAttempt(ctx)

	if strings.TrimSpace(model) == "" {
		model = c.model
	}

	if c.apiKey == "" {
		if c.observer != nil {
			errStr := ErrNotConfigured.Error()
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             0,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", ErrNotConfigured
	}

	normalizedContent, err := normalizeContent(content)
	if err != nil {
		return "", err
	}

	messages := make([]Message, 0, len(history)+2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, Message{Role: "system", Content: systemPrompt})
	}
	for _, message := range history {
		if message.Role != "user" && message.Role != "assistant" {
			return "", fmt.Errorf("openrouter: invalid history role %q", message.Role)
		}
		messages = append(messages, message)
	}
	messages = append(messages, Message{Role: "user", Content: normalizedContent})
	body, err := json.Marshal(chatRequest{Model: model, SessionID: SessionID(ctx), Messages: messages})
	if err != nil {
		return "", fmt.Errorf("openrouter: marshal request: %w", err)
	}

	targetURL := c.apiURL
	if targetURL == "" {
		targetURL = apiURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("openrouter: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "Jasmine")
	req.Header.Set("HTTP-Referer", "https://github.com/jasmine-preview")

	resp, err := c.http.Do(req)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		if c.observer != nil {
			errStr := err.Error()
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             latency,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", fmt.Errorf("openrouter: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.observer != nil {
			errStr := err.Error()
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             latency,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", fmt.Errorf("openrouter: read body: %w", err)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		if c.observer != nil {
			errStr := fmt.Sprintf("decode error: %v", err)
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             latency,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", fmt.Errorf("openrouter: decode response: %w", err)
	}

	if cr.Error != nil {
		if c.observer != nil {
			errStr := fmt.Sprintf("API error: %s", cr.Error.Message)
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             latency,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", fmt.Errorf("openrouter: API error: %s", cr.Error.Message)
	}

	if len(cr.Choices) == 0 {
		if c.observer != nil {
			errStr := "empty choices in response"
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             latency,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", fmt.Errorf("openrouter: empty choices in response")
	}

	text, err := decodeAssistantContent(cr.Choices[0].Message.Content)
	if err != nil {
		if c.observer != nil {
			errStr := err.Error()
			c.observer.RecordLLMCall(ctx, LLMCallRecord{
				CorrelationID:         corrID,
				UserID:                userID,
				Operation:             operation,
				Provider:              "openrouter",
				Model:                 model,
				LatencyMs:             latency,
				Success:               false,
				ErrorMessage:          &errStr,
				RetryCount:            attempt,
				StructuredOutputValid: false,
				CreatedAt:             time.Now(),
			})
		}
		return "", err
	}

	// Success at transport level
	if c.observer != nil {
		var inTokens, outTokens *int
		if cr.Usage != nil {
			inTokens = &cr.Usage.PromptTokens
			outTokens = &cr.Usage.CompletionTokens
		}
		c.observer.RecordLLMCall(ctx, LLMCallRecord{
			CorrelationID:         corrID,
			UserID:                userID,
			Operation:             operation,
			Provider:              "openrouter",
			Model:                 model,
			LatencyMs:             latency,
			Success:               true,
			ErrorMessage:          nil,
			RetryCount:            attempt,
			InputTokens:           inTokens,
			OutputTokens:          outTokens,
			StructuredOutputValid: true,
			CreatedAt:             time.Now(),
		})
	}

	return text, nil
}

func normalizeContent(content any) (any, error) {
	switch v := content.(type) {
	case string:
		return v, nil
	case []ContentPart:
		return v, nil
	default:
		return nil, fmt.Errorf("openrouter: unsupported content type %T", content)
	}
}

func decodeAssistantContent(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	var parts []ContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("openrouter: unsupported response content format")
	}

	var sb strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			sb.WriteString(part.Text)
		}
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("openrouter: empty text content in response")
	}
	return sb.String(), nil
}
