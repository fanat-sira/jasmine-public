package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	telegramclient "jasmine/internal/clients/telegram"
)

// StartWebhook registers the configured Telegram webhook on every app start.
func (s *Service) StartWebhook(ctx context.Context, url, secret string) {
	if s.client == nil || url == "" {
		return
	}
	for ctx.Err() == nil {
		if err := s.client.SetWebhook(ctx, url, secret); err == nil {
			s.logger().Info("telegram webhook registered")
			return
		} else {
			s.logger().Warn("telegram webhook registration failed; retrying", slog.String("error", err.Error()))
		}
		if !waitTelegramRetry(ctx) {
			return
		}
	}
}

// StartPolling receives Telegram updates when a public webhook is unavailable.
func (s *Service) StartPolling(ctx context.Context) {
	if s.client == nil {
		return
	}
	for ctx.Err() == nil {
		if err := s.client.DeleteWebhook(ctx); err == nil {
			break
		} else {
			s.logger().Warn("telegram webhook removal failed; retrying", slog.String("error", err.Error()))
		}
		if !waitTelegramRetry(ctx) {
			return
		}
	}
	var offset int64
	for ctx.Err() == nil {
		updates, err := s.client.GetUpdatesRaw(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger().Warn("telegram polling failed; retrying", slog.String("error", err.Error()))
			if !waitTelegramRetry(ctx) {
				return
			}
			continue
		}
		failed := false
		for _, raw := range updates {
			var update telegramclient.Update
			if err := json.Unmarshal(raw, &update); err != nil || update.UpdateID == 0 {
				s.logger().Warn("telegram polling received malformed update")
				continue
			}
			if err := s.ProcessWebhookUpdate(ctx, raw); err != nil {
				s.logger().Error("telegram polled update failed; retrying", slog.Int64("update_id", update.UpdateID), slog.String("error", err.Error()))
				failed = true
				break
			}
			offset = update.UpdateID + 1
		}
		if failed && !waitTelegramRetry(ctx) {
			return
		}
	}
}

func waitTelegramRetry(ctx context.Context) bool {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
