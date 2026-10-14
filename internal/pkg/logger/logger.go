package logger

import (
	"log/slog"
	"os"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Setup настраивает глобальный slog-логгер под нужное окружение:
//   - development: TextHandler с уровнем Debug (читаемый вывод в терминале)
//   - production:  JSONHandler с уровнем Info (для Loki/CloudWatch)
func Setup(env string) *slog.Logger {
	var handler slog.Handler

	switch env {
	case EnvProduction:
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
	default:
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level:     slog.LevelDebug,
			AddSource: true,
		})
	}

	log := slog.New(handler)
	slog.SetDefault(log)
	return log
}
