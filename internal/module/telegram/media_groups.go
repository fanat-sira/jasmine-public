package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const MediaGroupQuietWindow = 1500 * time.Millisecond
const MaxAlbumPhotos = 10

type AlbumImage struct {
	UpdateID  int64      `json:"update_id"`
	MessageID int64      `json:"message_id"`
	Image     ImageInput `json:"image"`
}
type AlbumInput struct {
	MediaGroupID string       `json:"media_group_id"`
	Caption      string       `json:"caption"`
	Items        []AlbumImage `json:"items"`
}
type AlbumIngestionHandler interface {
	IngestAlbum(ctx context.Context, userID string, chatID int64, album AlbumInput) (*IngestionResponse, error)
}
type MediaGroup struct {
	UserID         string
	ChatID         int64
	ID             string
	Caption        string
	FirstUpdateID  int64
	FirstMessageID int64
	Attempts       int
}

type MediaGroupItem struct {
	UpdateID     int64
	MessageID    int64
	FileID       string
	FileUniqueID string
	Caption      string
}

func (r *Repository) AddMediaGroupItem(ctx context.Context, userID string, chatID int64, groupID string, updateID, messageID int64, fileID, fileUniqueID, caption string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	groupTag, err := tx.Exec(ctx, `INSERT INTO telegram_media_groups(user_id,media_group_id,first_update_id,chat_id,caption,first_message_id,status,last_seen_at)
      VALUES($1,$2,$3,$4,$5,$6,'collecting',NOW())
      ON CONFLICT(user_id,media_group_id) DO UPDATE SET last_seen_at=NOW(),
        caption=CASE WHEN EXCLUDED.caption<>'' THEN EXCLUDED.caption ELSE telegram_media_groups.caption END,
        first_update_id=LEAST(telegram_media_groups.first_update_id,EXCLUDED.first_update_id),
        first_message_id=LEAST(telegram_media_groups.first_message_id,EXCLUDED.first_message_id)
      WHERE telegram_media_groups.status='collecting'`, userID, groupID, updateID, chatID, caption, messageID)
	if err != nil {
		return fmt.Errorf("upsert media group: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO telegram_media_group_items(user_id,media_group_id,update_id,message_id,file_id,file_unique_id,caption)
       VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(update_id) DO NOTHING`, userID, groupID, updateID, messageID, fileID, fileUniqueID, caption)
	if err != nil {
		return fmt.Errorf("insert media group item: %w", err)
	}
	if groupTag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `UPDATE telegram_updates SET status='completed',response_payload='{"action":"late_album_member"}'::jsonb,completed_at=NOW(),locked_by=NULL WHERE update_id=$1`, updateID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) ClaimReadyMediaGroup(ctx context.Context, now time.Time) (*MediaGroup, error) {
	g := &MediaGroup{}
	err := r.db.QueryRow(ctx, `WITH candidate AS (
      SELECT user_id,media_group_id FROM telegram_media_groups
      WHERE (((status='collecting' OR status='failed') AND last_seen_at<=$1)
         OR (status='processing' AND locked_at < NOW()-INTERVAL '2 minutes'))
      AND attempts<5
      ORDER BY last_seen_at FOR UPDATE SKIP LOCKED LIMIT 1)
      UPDATE telegram_media_groups g SET status='processing',locked_at=NOW(),attempts=attempts+1
      FROM candidate c WHERE g.user_id=c.user_id AND g.media_group_id=c.media_group_id
      RETURNING g.user_id,g.chat_id,g.media_group_id,g.caption,g.first_update_id,g.first_message_id,g.attempts`, now.Add(-MediaGroupQuietWindow)).Scan(&g.UserID, &g.ChatID, &g.ID, &g.Caption, &g.FirstUpdateID, &g.FirstMessageID, &g.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}
func (r *Repository) ListMediaGroupItems(ctx context.Context, g *MediaGroup) ([]MediaGroupItem, error) {
	rows, err := r.db.Query(ctx, `SELECT update_id,message_id,file_id,file_unique_id,caption FROM telegram_media_group_items
      WHERE user_id=$1 AND media_group_id=$2 ORDER BY message_id`, g.UserID, g.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MediaGroupItem{}
	for rows.Next() {
		var item MediaGroupItem
		if err := rows.Scan(&item.UpdateID, &item.MessageID, &item.FileID, &item.FileUniqueID, &item.Caption); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *Repository) FailMediaGroup(ctx context.Context, g *MediaGroup, reason string) error {
	_, err := r.db.Exec(ctx, `UPDATE telegram_media_groups SET status='failed',locked_at=NULL WHERE user_id=$1 AND media_group_id=$2`, g.UserID, g.ID)
	return err
}
func (r *Repository) CompleteMediaGroup(ctx context.Context, g *MediaGroup, res *IngestionResponse) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if res == nil || !res.Success {
		facts := replyContextForIngestionResponse(res)
		var refs []DomainReference
		if res != nil {
			refs = res.DomainReferences
		}
		payload, err := json.Marshal(OutboxPayload{Text: "", ReplyContext: facts, ParseMode: "HTML", ReplyToMessageID: &g.FirstMessageID, DomainReferences: refs})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO telegram_deliveries(update_id,chat_id,user_id,status,payload)
          SELECT $1,$2,$3,'pending',$4::jsonb WHERE NOT EXISTS(SELECT 1 FROM telegram_deliveries WHERE update_id=$1)`, g.FirstUpdateID, g.ChatID, g.UserID, string(payload))
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE telegram_updates SET status='completed',response_payload=$3::jsonb,completed_at=NOW(),locked_by=NULL
       WHERE update_id IN (SELECT update_id FROM telegram_media_group_items WHERE user_id=$1 AND media_group_id=$2)`, g.UserID, g.ID, `{"action":"album_processed"}`)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE telegram_media_groups SET status='processed',processed_at=NOW(),locked_at=NULL WHERE user_id=$1 AND media_group_id=$2 AND status='processing'`, g.UserID, g.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("media group is not processing")
	}
	return tx.Commit(ctx)
}
