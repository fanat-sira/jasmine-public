package assistant

import (
	"context"
	"path/filepath"
	"strings"

	"jasmine/internal/module/telegram"
)

func profileDocument(doc telegram.DocumentInput) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(doc.Filename)))
	if name == "profile.md" || name == "profile.markdown" || name == "profile.json" || name == "profile.txt" || strings.HasPrefix(name, "profile_") || strings.HasPrefix(name, "профиль") {
		return true
	}
	caption := strings.ToLower(strings.ReplaceAll(doc.Caption, "ё", "е"))
	for _, phrase := range []string{"это мой профиль", "импортируй как профиль", "информация обо мне", "добавь в мой профиль", "запомни обо мне"} {
		if strings.Contains(caption, phrase) {
			return true
		}
	}
	return false
}

// IngestDocument receives only a Telegram document; image and food handlers are never called here.
func (o *Orchestrator) IngestDocument(ctx context.Context, userID string, chatID, updateID, messageID int64, _ *int64, doc telegram.DocumentInput) (*telegram.IngestionResponse, error) {
	if doc.Kind == telegram.DocumentOther {
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "document_unsupported", CurrentState: map[string]any{"filename": doc.Filename, "mime_type": doc.MIMEType}}}, nil
	}
	if !profileDocument(doc) {
		return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "document_received", CurrentState: map[string]any{"filename": doc.Filename, "kind": doc.Kind, "bytes": len(doc.Text)}}}, nil
	}
	if doc.Kind == telegram.DocumentJSON {
		return o.saveProfileJSON(ctx, userID, chatID, updateID, messageID, doc.Text, &doc)
	}
	return o.saveProfileMarkdown(telegram.MarkProfileMarkdownUpload(ctx), userID, chatID, updateID, messageID, doc.Text, &doc)
}
