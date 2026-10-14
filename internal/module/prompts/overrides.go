package prompts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"unicode/utf8"
)

// LoadOverrides reads operator-owned templates. Errors never contain template text.
func LoadOverrides(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read private prompt file")
	}
	var overrides map[string]string
	if !utf8.Valid(raw) || json.Unmarshal(raw, &overrides) != nil || len(overrides) == 0 {
		return nil, errors.New("private prompt file must be a nonempty UTF-8 JSON object of templates")
	}
	for key, template := range overrides {
		if _, ok := Defaults[key]; !ok || strings.TrimSpace(template) == "" {
			return nil, errors.New("private prompt file contains an unknown key or empty template")
		}
	}
	return overrides, nil
}

// ApplyOverridesFile atomically stores explicit operator settings before defaults
// are seeded. Removing the file setting does not erase stored database overrides.
func (r *Repository) ApplyOverridesFile(ctx context.Context, path string) error {
	overrides, err := LoadOverrides(path)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return errors.New("cannot begin private prompt update")
	}
	defer tx.Rollback(ctx)
	for key, template := range overrides {
		_, err = tx.Exec(ctx, `INSERT INTO system_prompts(key,template,content,updated_at) VALUES($1,$2,$2,NOW())
ON CONFLICT(key) DO UPDATE SET template=EXCLUDED.template,content=EXCLUDED.content,updated_at=NOW()`, key, template)
		if err != nil {
			return errors.New("cannot store private prompt overrides")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("cannot commit private prompt overrides")
	}
	return nil
}
