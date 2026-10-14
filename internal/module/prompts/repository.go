package prompts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPromptNotFound = errors.New("system prompt not found")

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetPrompt(ctx context.Context, key string) (*SystemPrompt, error) {
	prompt := &SystemPrompt{}
	err := r.db.QueryRow(ctx,
		`SELECT key, template, updated_at
		 FROM system_prompts
		 WHERE key = $1`,
		key,
	).Scan(&prompt.Key, &prompt.Template, &prompt.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPromptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ai repo: get prompt: %w", err)
	}
	return prompt, nil
}

// EnsurePromptDefaults adds missing prompt rows without overwriting database overrides.
func (r *Repository) EnsurePromptDefaults(ctx context.Context) error {
	keys := make([]string, 0, len(Defaults))
	for key := range Defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, err := r.db.Exec(ctx, `INSERT INTO system_prompts(key,template,content,description) VALUES($1,$2,$2,'Jasmine prompt catalog') ON CONFLICT(key) DO NOTHING`, key, Default(key))
		if err != nil {
			return fmt.Errorf("ensure prompt %s: %w", key, err)
		}
		if fingerprint := LegacySeedSHA256[key]; fingerprint != "" {
			current, err := r.GetPromptTemplate(ctx, key)
			if err != nil {
				return err
			}
			hash := sha256.Sum256([]byte(current))
			if hex.EncodeToString(hash[:]) == fingerprint {
				_, err = r.db.Exec(ctx, `UPDATE system_prompts SET template=$2, content=$2, updated_at=NOW() WHERE key=$1 AND template=$3`, key, Default(key), current)
				if err != nil {
					return fmt.Errorf("upgrade historical prompt %s: %w", key, err)
				}
			}
		}

	}
	return nil
}

func (r *Repository) GetPromptTemplate(ctx context.Context, key string) (string, error) {
	p, err := r.GetPrompt(ctx, key)
	if errors.Is(err, ErrPromptNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return p.Template, nil
}

type SystemPrompt struct {
	Key       string
	Template  string
	UpdatedAt time.Time
}

// SavePrompt stores an explicit operator override. No HTTP editor is exposed.
func (r *Repository) SavePrompt(ctx context.Context, key, template string) (*SystemPrompt, error) {
	p := &SystemPrompt{}
	err := r.db.QueryRow(ctx, `INSERT INTO system_prompts(key,template,content,updated_at) VALUES($1,$2,$2,NOW())
 ON CONFLICT(key) DO UPDATE SET template=EXCLUDED.template,content=EXCLUDED.content,updated_at=NOW()
 RETURNING key,template,updated_at`, key, template).Scan(&p.Key, &p.Template, &p.UpdatedAt)
	return p, err
}
