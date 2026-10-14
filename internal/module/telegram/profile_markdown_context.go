package telegram

import "context"

type markdownDocumentContextKey struct{}

// MarkProfileMarkdownUpload limits document extraction to a real Telegram upload.
func MarkProfileMarkdownUpload(ctx context.Context) context.Context {
	return context.WithValue(ctx, markdownDocumentContextKey{}, true)
}
func IsProfileMarkdownUpload(ctx context.Context) bool {
	allowed, _ := ctx.Value(markdownDocumentContextKey{}).(bool)
	return allowed
}
