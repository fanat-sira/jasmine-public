package observability

import (
	"regexp"
	"strings"
	"time"
)

type LLMCallLog struct {
	ID                    string    `json:"id"`
	CorrelationID         string    `json:"correlation_id"`
	UserID                string    `json:"user_id"`
	Operation             string    `json:"operation"` // workout_draft_extraction, correction_extraction, memory_extraction, workout_generation, disambiguation
	Provider              string    `json:"provider"`
	Model                 string    `json:"model"`
	LatencyMs             int64     `json:"latency_ms"`
	Success               bool      `json:"success"`
	ErrorMessage          *string   `json:"error_message,omitempty"`
	RetryCount            int       `json:"retry_count"`
	InputTokens           *int      `json:"input_tokens,omitempty"`
	OutputTokens          *int      `json:"output_tokens,omitempty"`
	StructuredOutputValid bool      `json:"structured_output_valid"`
	CreatedAt             time.Time `json:"created_at"`
}

// Centralized Sensitive Data Redaction
var (
	tokenPattern        = regexp.MustCompile(`(?i)(bot\d+:[A-Za-z0-9_-]+|sk-or-[A-Za-z0-9_-]+|Bearer\s+[A-Za-z0-9._-]+|password["':\s]+[^\s",}]+|secret["':\s]+[^\s",}]+)`)
	passwordHashPattern = regexp.MustCompile(`\$2[ayb]\$[0-9]{2}\$[A-Za-z0-9./]{53}`)
)

// SanitizeString cleans sensitive credentials, tokens, and password hashes from any text.
func SanitizeString(s string) string {
	if s == "" {
		return s
	}
	s = passwordHashPattern.ReplaceAllString(s, "[REDACTED_PASSWORD_HASH]")
	s = tokenPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(strings.ToLower(m), "bot") {
			return "bot[REDACTED_BOT_TOKEN]"
		}
		if strings.HasPrefix(strings.ToLower(m), "sk-or-") {
			return "sk-or-[REDACTED_API_KEY]"
		}
		if strings.HasPrefix(strings.ToLower(m), "bearer") {
			return "Bearer [REDACTED_JWT]"
		}
		return "[REDACTED_SECRET]"
	})
	return s
}
