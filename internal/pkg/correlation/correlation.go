package correlation

import (
	"context"
	"fmt"
	"time"
)

type contextKey string

const (
	correlationIDKey contextKey = "correlation_id"
	operationKey     contextKey = "operation"
	attemptKey       contextKey = "attempt"
)

// GenerateUpdateCorrelationID creates a deterministic root correlation ID from Telegram update ID.
func GenerateUpdateCorrelationID(updateID int64) string {
	return fmt.Sprintf("upd-%d", updateID)
}

// GenerateID creates a timestamped correlation ID.
func GenerateID(prefix string) string {
	if prefix == "" {
		prefix = "req"
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// WithCorrelationID sets the correlation ID on context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// GetCorrelationID extracts the correlation ID from context, or empty string.
func GetCorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(correlationIDKey).(string); ok {
		return v
	}
	return ""
}

// WithOperation sets the current LLM/domain operation on context.
func WithOperation(ctx context.Context, op string) context.Context {
	return context.WithValue(ctx, operationKey, op)
}

// GetOperation extracts the operation name from context.
func GetOperation(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(operationKey).(string); ok {
		return v
	}
	return ""
}

// WithAttempt sets the current attempt count on context.
func WithAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, attemptKey, attempt)
}

// GetAttempt extracts the attempt count from context.
func GetAttempt(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(attemptKey).(int); ok {
		return v
	}
	return 0
}
