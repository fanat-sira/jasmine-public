package openrouter

import (
	"context"
	"strings"
)

type sessionIDKey struct{}

// WithSessionID sets a stable OpenRouter sticky-routing key for one conversation.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 256 {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

func SessionID(ctx context.Context) string {
	id, _ := ctx.Value(sessionIDKey{}).(string)
	return id
}
