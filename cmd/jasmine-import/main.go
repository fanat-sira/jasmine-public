package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"jasmine/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

type catalogFile struct {
	Exercises []catalogExercise `json:"exercises"`
}

type catalogExercise struct {
	ExerciseID          string   `json:"exercise_id"`
	NameEN              string   `json:"name_en"`
	NameRU              string   `json:"name_ru"`
	AliasesEN           []string `json:"aliases_en"`
	AliasesRU           []string `json:"aliases_ru"`
	Category            string   `json:"category"`
	BodyRegion          string   `json:"body_region"`
	MovementPattern     string   `json:"movement_pattern"`
	Equipment           []string `json:"equipment"`
	Environment         []string `json:"environment"`
	PrimaryMuscles      []string `json:"primary_muscles"`
	SecondaryMuscles    []string `json:"secondary_muscles"`
	Difficulty          string   `json:"difficulty"`
	TechnicalComplexity int      `json:"technical_complexity"`
	ImpactLevel         string   `json:"impact_level"`
	IsUnilateral        bool     `json:"is_unilateral"`
	IsCardio            bool     `json:"is_cardio"`
	TrackingType        string   `json:"tracking_type"`
	ScoreWeight         int      `json:"score_weight"`
	FatigueCost         int      `json:"fatigue_cost"`
	InjuryRisk          string   `json:"injury_risk"`
	Notes               string   `json:"notes"`
}

type options struct {
	dbURL         string
	file          string
	source        string
	replaceSource bool
	dryRun        bool
}

func main() {
	opts := parseFlags()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	payload, err := loadCatalog(opts.file)
	if err != nil {
		log.Fatalf("import: %v", err)
	}

	if opts.dryRun {
		log.Printf("dry-run: parsed %d exercises from %s", len(payload.Exercises), opts.file)
		return
	}

	pool, err := storage.NewPostgres(ctx, opts.dbURL)
	if err != nil {
		log.Fatalf("import: connect postgres: %v", err)
	}
	defer pool.Close()

	if err := ensureCatalogSchema(ctx, pool); err != nil {
		log.Fatalf("import: %v", err)
	}

	if err := upsertCatalog(ctx, pool, payload.Exercises, opts.source, opts.replaceSource); err != nil {
		log.Fatalf("import: %v", err)
	}

	log.Printf("import complete: %d exercises from %s", len(payload.Exercises), opts.file)
}

func parseFlags() options {
	var opts options

	defaultDBURL := strings.TrimSpace(os.Getenv("DB_URL"))
	flag.StringVar(&opts.dbURL, "db-url", defaultDBURL, "PostgreSQL DSN (or set DB_URL env)")
	flag.StringVar(&opts.file, "file", "fitness_exercises_catalog_ru_en.json", "Path to exercise catalog JSON")
	flag.StringVar(&opts.source, "source", "catalog", "Source label for imported rows")
	flag.BoolVar(&opts.replaceSource, "replace-source", false, "Delete existing rows with this source before import")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "Parse file and validate only (no DB writes)")
	flag.Parse()

	if !opts.dryRun && strings.TrimSpace(opts.dbURL) == "" {
		log.Fatal("import: DB URL is required (flag -db-url or env DB_URL)")
	}
	return opts
}

func loadCatalog(path string) (*catalogFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	var payload catalogFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	if len(payload.Exercises) == 0 {
		return nil, fmt.Errorf("catalog is empty")
	}
	return &payload, nil
}

func ensureCatalogSchema(ctx context.Context, db *pgxpool.Pool) error {
	var hasColumn bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'exercises'
			  AND column_name = 'exercise_code'
		)`,
	).Scan(&hasColumn)
	if err != nil {
		return fmt.Errorf("check schema: %w", err)
	}
	if !hasColumn {
		return fmt.Errorf("missing exercises.exercise_code. Run migrations first (including 000017)")
	}
	return nil
}

func upsertCatalog(ctx context.Context, db *pgxpool.Pool, items []catalogExercise, source string, replace bool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if replace {
		if _, err := tx.Exec(ctx, `DELETE FROM exercises WHERE source = $1`, source); err != nil {
			return fmt.Errorf("delete existing source=%q: %w", source, err)
		}
	}

	const stmt = `
		INSERT INTO exercises (
			exercise_code, name, name_en, name_ru,
			aliases_en, aliases_ru,
			type, muscle_group,
			category, body_region, movement_pattern,
			equipment, environment, primary_muscles, secondary_muscles,
			difficulty, technical_complexity, impact_level,
			is_unilateral, is_cardio,
			tracking_type, score_weight, fatigue_cost, injury_risk,
			notes, source, is_default, updated_at
		)
		VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8,
			$9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18,
			$19, $20,
			$21, $22, $23, $24,
			$25, $26, $27, NOW()
		)
		ON CONFLICT (exercise_code) DO UPDATE
		SET
			name                 = EXCLUDED.name,
			name_en              = EXCLUDED.name_en,
			name_ru              = EXCLUDED.name_ru,
			aliases_en           = EXCLUDED.aliases_en,
			aliases_ru           = EXCLUDED.aliases_ru,
			type                 = EXCLUDED.type,
			muscle_group         = EXCLUDED.muscle_group,
			category             = EXCLUDED.category,
			body_region          = EXCLUDED.body_region,
			movement_pattern     = EXCLUDED.movement_pattern,
			equipment            = EXCLUDED.equipment,
			environment          = EXCLUDED.environment,
			primary_muscles      = EXCLUDED.primary_muscles,
			secondary_muscles    = EXCLUDED.secondary_muscles,
			difficulty           = EXCLUDED.difficulty,
			technical_complexity = EXCLUDED.technical_complexity,
			impact_level         = EXCLUDED.impact_level,
			is_unilateral        = EXCLUDED.is_unilateral,
			is_cardio            = EXCLUDED.is_cardio,
			tracking_type        = EXCLUDED.tracking_type,
			score_weight         = EXCLUDED.score_weight,
			fatigue_cost         = EXCLUDED.fatigue_cost,
			injury_risk          = EXCLUDED.injury_risk,
			notes                = EXCLUDED.notes,
			source               = EXCLUDED.source,
			is_default           = EXCLUDED.is_default,
			updated_at           = NOW()
	`

	for i, e := range items {
		code := normalizeToken(e.ExerciseID)
		if code == "" {
			return fmt.Errorf("item %d: exercise_id is required", i)
		}

		// Keep legacy `name` deterministic and unique-safe.
		legacyName := code

		if _, err := tx.Exec(ctx, stmt,
			code,
			legacyName,
			normalizeTitle(e.NameEN, code),
			normalizeTitle(e.NameRU, code),
			normalizeStringSlice(e.AliasesEN),
			normalizeStringSlice(e.AliasesRU),
			mapLegacyType(e.Category, e.IsCardio),
			normalizeTokenOrDefault(e.BodyRegion, "full_body"),
			normalizeTokenOrDefault(e.Category, "strength"),
			normalizeTokenOrDefault(e.BodyRegion, "full_body"),
			normalizeToken(e.MovementPattern),
			normalizeStringSlice(e.Equipment),
			normalizeStringSlice(e.Environment),
			normalizeStringSlice(e.PrimaryMuscles),
			normalizeStringSlice(e.SecondaryMuscles),
			normalizeToken(e.Difficulty),
			e.TechnicalComplexity,
			normalizeToken(e.ImpactLevel),
			e.IsUnilateral,
			e.IsCardio,
			normalizeToken(e.TrackingType),
			e.ScoreWeight,
			e.FatigueCost,
			normalizeToken(e.InjuryRisk),
			strings.TrimSpace(e.Notes),
			source,
			true,
		); err != nil {
			return fmt.Errorf("upsert %q: %w", code, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func normalizeToken(in string) string {
	trimmed := strings.TrimSpace(strings.ToLower(in))
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer(" ", "_", "-", "_")
	normalized := replacer.Replace(trimmed)
	normalized = strings.Join(strings.FieldsFunc(normalized, func(r rune) bool {
		return r == '_' || r == '/'
	}), "_")
	return normalized
}

func normalizeTokenOrDefault(in string, fallback string) string {
	v := normalizeToken(in)
	if v == "" {
		return fallback
	}
	return v
}

func normalizeTitle(in string, fallback string) string {
	trimmed := strings.TrimSpace(in)
	if trimmed != "" {
		return trimmed
	}
	return fallback
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, item := range values {
		normalized := normalizeToken(item)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return []string{}
	}
	return out
}

func mapLegacyType(category string, isCardio bool) string {
	if isCardio {
		return "cardio"
	}
	switch normalizeToken(category) {
	case "cardio", "conditioning":
		return "cardio"
	default:
		return "strength"
	}
}
