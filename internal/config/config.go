package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	Env                 string `env:"ENV"                   env-default:"development"`
	DBURL               string `env:"DB_URL"                env-required:"true"`
	OFFEnabled          bool   `env:"OFF_ENABLED"           env-default:"true"`
	PromptOverridesFile string `env:"JASMINE_PROMPT_OVERRIDES_FILE" env-default:""`
	OwnerTelegramID     int64  `env:"JASMINE_OWNER_TELEGRAM_ID" env-default:"0"`
	HTTP                HTTPConfig
	OpenRouter          OpenRouterConfig
	Telegram            TelegramConfig
}

type TelegramConfig struct {
	BotToken       string `env:"TELEGRAM_BOT_TOKEN"    env-default:""`
	WebhookURL     string `env:"TELEGRAM_WEBHOOK_URL"  env-default:""`
	SecretToken    string `env:"TELEGRAM_SECRET_TOKEN" env-default:""`
	PollingMode    bool   `env:"TELEGRAM_POLLING_MODE" env-default:"false"`
	AllowedUserIDs string `env:"TELEGRAM_ALLOWED_USER_IDS" env-default:""`
}

type OpenRouterConfig struct {
	APIKey     string `env:"OPENROUTER_API_KEY" env-default:""`
	MainModel  string `env:"LLM_MAIN" env-default:"google/gemini-3.8-flash"`
	PhotoModel string `env:"LLM_PHOTO" env-default:"google/gemini-3.8-flash"`
	ReplyModel string `env:"LLM_REPLY" env-default:"x-ai/grok-4.7"`
}

type HTTPConfig struct {
	Host         string        `env:"HTTP_HOST"          env-default:"0.0.0.0"`
	Port         string        `env:"SERVER_PORT"        env-default:"8080"`
	ReadTimeout  time.Duration `env:"HTTP_READ_TIMEOUT"  env-default:"10s"`
	WriteTimeout time.Duration `env:"HTTP_WRITE_TIMEOUT" env-default:"10s"`
	IdleTimeout  time.Duration `env:"HTTP_IDLE_TIMEOUT"  env-default:"60s"`
}

func (h HTTPConfig) Addr() string {
	return fmt.Sprintf("%s:%s", h.Host, h.Port)
}

// AllowedTelegramUserIDs parses the required comma-separated Telegram user allowlist.
// An empty value is rejected; the personal bot never defaults to public access.
func (t TelegramConfig) AllowedTelegramUserIDs() ([]int64, error) {
	if strings.TrimSpace(t.AllowedUserIDs) == "" {
		return nil, fmt.Errorf("TELEGRAM_ALLOWED_USER_IDS must contain at least one allowed user ID")
	}
	parts := strings.Split(t.AllowedUserIDs, ",")
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("TELEGRAM_ALLOWED_USER_IDS must contain positive numeric Telegram user IDs separated by commas")
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (c *Config) Validate() error {
	ids, err := c.Telegram.AllowedTelegramUserIDs()
	if err != nil {
		return err
	}
	if c.OwnerTelegramID != 0 {
		allowed := false
		for _, id := range ids {
			if id == c.OwnerTelegramID {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("JASMINE_OWNER_TELEGRAM_ID must be in TELEGRAM_ALLOWED_USER_IDS")
		}
	}

	if strings.TrimSpace(c.Telegram.BotToken) == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if !c.Telegram.PollingMode {
		if strings.TrimSpace(c.Telegram.WebhookURL) == "" {
			return fmt.Errorf("TELEGRAM_WEBHOOK_URL is required in webhook mode")
		}
		if strings.TrimSpace(c.Telegram.SecretToken) == "" {
			return fmt.Errorf("TELEGRAM_SECRET_TOKEN is required in webhook mode")
		}
	}
	if c.Env == "production" && c.OpenRouter.APIKey == "" {
		return fmt.Errorf("OPENROUTER_API_KEY is required in production")
	}

	return nil
}

func MustLoad() *Config {
	cfg := &Config{}
	if err := cleanenv.ReadEnv(cfg); err != nil {
		panic(fmt.Sprintf("config: %v", err))
	}
	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("config validation failed: %v", err))
	}
	return cfg
}
