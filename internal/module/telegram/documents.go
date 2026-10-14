package telegram

import (
	"context"
	"path/filepath"
	"strings"
)

type DocumentKind string

const (
	DocumentPhoto DocumentKind = "photo"
	DocumentJSON  DocumentKind = "json_document"
	DocumentText  DocumentKind = "text_document"
	DocumentOther DocumentKind = "other_document"
)

type DocumentInput struct {
	Kind         DocumentKind `json:"kind"`
	Filename     string       `json:"filename"`
	MIMEType     string       `json:"mime_type"`
	FileID       string       `json:"telegram_file_id"`
	FileUniqueID string       `json:"telegram_file_unique_id"`
	MessageID    int64        `json:"telegram_message_id"`
	Caption      string       `json:"caption,omitempty"`
	Text         string       `json:"-"`
}
type DocumentIngestionHandler interface {
	IngestDocument(context.Context, string, int64, int64, int64, *int64, DocumentInput) (*IngestionResponse, error)
}

func ClassifyDocument(filename, mime string) DocumentKind {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	if ext == ".json" {
		return DocumentJSON
	}
	if ext == ".md" || ext == ".markdown" || ext == ".txt" {
		return DocumentText
	}
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" {
		return DocumentPhoto
	}
	if mime == "application/json" {
		return DocumentJSON
	}
	if mime == "text/markdown" || mime == "text/plain" {
		return DocumentText
	}
	if mime == "image/jpeg" || mime == "image/png" || mime == "image/webp" || mime == "image/gif" {
		return DocumentPhoto
	}
	return DocumentOther
}
