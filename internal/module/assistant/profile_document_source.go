package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"jasmine/internal/module/telegram"
	"github.com/jackc/pgx/v5"
)

func savePersonalDocumentTx(ctx context.Context, tx pgx.Tx, userID string, updateID, messageID int64, raw string, source ...*telegram.DocumentInput) (*string, error) {
	if len(source) == 0 || source[0] == nil {
		return nil, nil
	}
	doc := source[0]
	format := "text"
	switch strings.ToLower(filepath.Ext(doc.Filename)) {
	case ".json":
		format = "json"
	case ".md", ".markdown":
		format = "markdown"
	}
	hash := sha256.Sum256([]byte(raw))
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO personal_documents(user_id,source_update_id,source_message_id,telegram_file_id,telegram_file_unique_id,filename,mime_type,format,content_sha256,content_text)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(source_update_id) DO UPDATE SET source_update_id=EXCLUDED.source_update_id RETURNING id`,
		userID, updateID, messageID, doc.FileID, doc.FileUniqueID, filepath.Base(doc.Filename), doc.MIMEType, format, hex.EncodeToString(hash[:]), raw).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
