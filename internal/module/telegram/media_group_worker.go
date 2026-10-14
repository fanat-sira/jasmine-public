package telegram

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ProcessReadyMediaGroups is safe to call from multiple workers. PostgreSQL claims
// each quiet group once; every source update remains linked to the logical turn.
func (s *Service) ProcessReadyMediaGroups(ctx context.Context, limit int) (int, error) {
	if s.repo == nil || s.client == nil || s.albumIngestionPipeline == nil {
		return 0, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	processed := 0
	for i := 0; i < limit; i++ {
		group, err := s.repo.ClaimReadyMediaGroup(ctx, time.Now())
		if err != nil {
			return processed, err
		}
		if group == nil {
			return processed, nil
		}
		if err := s.processMediaGroup(ctx, group); err != nil {
			if group.Attempts >= 5 {
				failure := &IngestionResponse{NeedsClarify: true, ReplyContext: &ReplyContext{EventType: "album_processing_failed", Clarification: []string{"album_images_unavailable_or_unreadable"}}}
				if finalErr := s.repo.CompleteMediaGroup(ctx, group, failure); finalErr != nil {
					_ = s.repo.FailMediaGroup(ctx, group, finalErr.Error())
					return processed, finalErr
				}
				processed++
				continue
			}
			_ = s.repo.FailMediaGroup(ctx, group, err.Error())
			s.logger().Error("media group processing failed", "media_group_id", group.ID, "error", err)
			return processed, err
		}
		processed++
	}
	return processed, nil
}
func (s *Service) StartMediaGroupWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.ProcessReadyMediaGroups(ctx, 10)
		}
	}
}
func (s *Service) processMediaGroup(ctx context.Context, g *MediaGroup) error {
	members, err := s.repo.ListMediaGroupItems(ctx, g)
	if err != nil {
		return err
	}
	if len(members) == 0 || len(members) > MaxAlbumPhotos {
		return fmt.Errorf("invalid album size: %d", len(members))
	}
	album := AlbumInput{MediaGroupID: g.ID, Caption: g.Caption, Items: make([]AlbumImage, 0, len(members))}
	for _, member := range members {
		file, err := s.client.GetFile(ctx, member.FileID)
		if err != nil {
			return err
		}
		data, err := s.client.DownloadFile(ctx, file, 10<<20)
		if err != nil {
			return err
		}
		mime := http.DetectContentType(data)
		if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" && mime != "image/gif" {
			return fmt.Errorf("unsupported album image type")
		}
		album.Items = append(album.Items, AlbumImage{UpdateID: member.UpdateID, MessageID: member.MessageID, Image: ImageInput{FileID: member.FileID, FileUniqueID: member.FileUniqueID, MIMEType: mime, Bytes: data}})
	}
	result, err := s.albumIngestionPipeline.IngestAlbum(ctx, g.UserID, g.ChatID, album)
	if err != nil {
		return err
	}
	return s.repo.CompleteMediaGroup(ctx, g, result)
}
