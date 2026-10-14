package identity

import "context"

type userIDKey struct{}

// WithUserID carries the domain user resolved from a trusted Telegram update.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}
func GetUserID(ctx context.Context) string { id, _ := ctx.Value(userIDKey{}).(string); return id }
