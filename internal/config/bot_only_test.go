package config

import (
	"testing"

	"github.com/ilyakaznacheev/cleanenv"
)

func TestBotConfigNeedsNoAccountOrRedisSettings(t *testing.T) {
	for _, key := range []string{"JWT_SECRET", "JWT_ACCESS_EXPIRY", "JWT_REFRESH_EXPIRY", "REDIS_URL", "ADMIN_BOOTSTRAP_EMAIL", "AUTH_REGISTRATION_ENABLED"} {
		t.Setenv(key, "")
	}
	t.Setenv("DB_URL", "postgres://localhost/synthetic")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "123")
	t.Setenv("TELEGRAM_BOT_TOKEN", "synthetic-test-value")
	t.Setenv("TELEGRAM_POLLING_MODE", "true")
	t.Setenv("ENV", "development")
	var cfg Config
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Telegram.PollingMode = false
	cfg.Telegram.WebhookURL = "https://example.test/webhook"
	cfg.Telegram.SecretToken = ""
	if cfg.Validate() == nil {
		t.Fatal("development webhook accepted missing secret")
	}
}

func TestOwnerPersonalityMustBelongToAllowedUser(t *testing.T) {
	cfg := Config{Telegram: TelegramConfig{AllowedUserIDs: "123,456", BotToken: "synthetic-test-value", PollingMode: true}, OwnerTelegramID: 123}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.OwnerTelegramID = 789
	if cfg.Validate() == nil {
		t.Fatal("private personality owner outside allowlist accepted")
	}
}
