package exercise

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("exercise not found")
	ErrAlreadyExists = errors.New("exercise already exists")
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) List(ctx context.Context) ([]Exercise, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id,
		        COALESCE(NULLIF(name_ru, ''), NULLIF(name_en, ''), name) AS display_name,
		        COALESCE(NULLIF(name_en, ''), name) AS name_en,
		        type, muscle_group,
		        COALESCE(category, '') AS category,
		        COALESCE(body_region, '') AS body_region,
		        COALESCE(tracking_type, '') AS tracking_type,
		        COALESCE(difficulty, '') AS difficulty,
		        COALESCE(equipment, '{}'::text[]) AS equipment,
		        COALESCE(is_cardio, false) AS is_cardio,
		        is_default, created_at
		 FROM exercises
		 ORDER BY is_default DESC, display_name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("exercise list: %w", err)
	}
	defer rows.Close()

	var out []Exercise
	for rows.Next() {
		var e Exercise
		if err := rows.Scan(
			&e.ID, &e.Name, &e.NameEN, &e.Type, &e.MuscleGroup,
			&e.Category, &e.BodyRegion, &e.TrackingType, &e.Difficulty, &e.Equipment, &e.IsCardio,
			&e.IsDefault, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("exercise list scan: %w", err)
		}
		out = append(out, e)
	}
	if out == nil {
		out = []Exercise{}
	}
	return out, rows.Err()
}

func (r *Repository) Search(ctx context.Context, query string) ([]Exercise, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return r.List(ctx)
	}
	expandedTerms := buildExerciseSearchTerms(trimmed)

	rows, err := r.db.Query(ctx,
		`WITH search_input AS (
		    SELECT
		        trim($1)::text AS raw_query,
		        lower(trim($1)) AS q_lower,
		        regexp_replace(lower(trim($1)), '[^a-zа-я0-9]+', ' ', 'g') AS normalized
		),
		expanded_terms AS (
		    SELECT DISTINCT term
		    FROM unnest($2::text[]) AS term
		    WHERE term <> ''
		)
		SELECT
		    e.id,
		    COALESCE(NULLIF(e.name_ru, ''), NULLIF(e.name_en, ''), e.name) AS display_name,
		    COALESCE(NULLIF(e.name_en, ''), e.name) AS name_en,
		    e.type,
		    e.muscle_group,
		    COALESCE(e.category, '') AS category,
		    COALESCE(e.body_region, '') AS body_region,
		    COALESCE(e.tracking_type, '') AS tracking_type,
		    COALESCE(e.difficulty, '') AS difficulty,
		    COALESCE(e.equipment, '{}'::text[]) AS equipment,
		    COALESCE(e.is_cardio, false) AS is_cardio,
		    e.is_default,
		    e.created_at
		FROM exercises e
		CROSS JOIN search_input si
		CROSS JOIN LATERAL (
		    SELECT
		        lower(
		            COALESCE(e.name_ru, '') || ' ' ||
		            COALESCE(e.name_en, '') || ' ' ||
		            COALESCE(e.name, '') || ' ' ||
		            COALESCE(e.exercise_code, '') || ' ' ||
		            COALESCE(e.type, '') || ' ' ||
		            COALESCE(e.muscle_group, '') || ' ' ||
		            COALESCE(e.category, '') || ' ' ||
		            COALESCE(e.body_region, '') || ' ' ||
		            COALESCE(e.movement_pattern, '') || ' ' ||
		            COALESCE(e.tracking_type, '') || ' ' ||
		            array_to_string(COALESCE(e.aliases_ru, '{}'::text[]), ' ') || ' ' ||
		            array_to_string(COALESCE(e.aliases_en, '{}'::text[]), ' ') || ' ' ||
		            array_to_string(COALESCE(e.primary_muscles, '{}'::text[]), ' ') || ' ' ||
		            array_to_string(COALESCE(e.secondary_muscles, '{}'::text[]), ' ') || ' ' ||
		            array_to_string(COALESCE(e.equipment, '{}'::text[]), ' ') || ' ' ||
		            array_to_string(COALESCE(e.environment, '{}'::text[]), ' ')
		        ) AS searchable_text
		) txt
		WHERE
		    lower(e.exercise_code) = si.q_lower
		    OR lower(e.name) = si.q_lower
		    OR lower(e.name_ru) = si.q_lower
		    OR lower(e.name_en) = si.q_lower
		    OR txt.searchable_text ILIKE '%' || si.raw_query || '%'
		    OR EXISTS (
		        SELECT 1
		        FROM expanded_terms et
		        WHERE txt.searchable_text ILIKE '%' || et.term || '%'
		    )
		    OR similarity(lower(txt.searchable_text), si.q_lower) >= 0.22
		    OR word_similarity(si.q_lower, lower(txt.searchable_text)) >= 0.33
		    OR NOT EXISTS (
		        SELECT 1
		        FROM regexp_split_to_table(si.normalized, '\s+') AS token
		        WHERE token <> ''
		          AND lower(txt.searchable_text) NOT LIKE '%' || token || '%'
		    )
		ORDER BY
		    (
		        CASE
		            WHEN lower(e.exercise_code) = si.q_lower THEN 160
		            WHEN lower(e.name) = si.q_lower OR lower(e.name_ru) = si.q_lower OR lower(e.name_en) = si.q_lower THEN 150
		            WHEN lower(COALESCE(NULLIF(e.name_ru, ''), NULLIF(e.name_en, ''), e.name)) = si.q_lower THEN 140
		            ELSE 0
		        END
		        +
		        CASE
		            WHEN txt.searchable_text ILIKE '%' || si.raw_query || '%' THEN 40
		            ELSE 0
		        END
			        +
			        (word_similarity(si.q_lower, lower(txt.searchable_text)) * 36.0)
			        +
			        COALESCE((
			            SELECT SUM(
			                CASE
		                    WHEN txt.searchable_text LIKE '%' || et.term || '%' THEN 18
		                    ELSE -4
		                END
		            )
		            FROM expanded_terms et
		        ), 0)
		        +
		        COALESCE((
		            SELECT SUM(
		                CASE
		                    WHEN token = '' THEN 0
		                    WHEN lower(txt.searchable_text) LIKE '%' || token || '%' THEN 14
		                    ELSE -8
		                END
		            )
		            FROM regexp_split_to_table(si.normalized, '\s+') AS token
		            WHERE token <> ''
		        ), 0)
		        +
		        (similarity(lower(txt.searchable_text), si.q_lower) * 30.0)
		    ) DESC,
		    e.is_default DESC,
		    display_name ASC
		LIMIT 100`,
		trimmed,
		expandedTerms,
	)
	if err != nil {
		return nil, fmt.Errorf("exercise search: %w", err)
	}
	defer rows.Close()

	out := make([]Exercise, 0)
	for rows.Next() {
		var e Exercise
		if err := rows.Scan(
			&e.ID, &e.Name, &e.NameEN, &e.Type, &e.MuscleGroup,
			&e.Category, &e.BodyRegion, &e.TrackingType, &e.Difficulty, &e.Equipment, &e.IsCardio,
			&e.IsDefault, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("exercise search scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var normalizeWhitespace = regexp.MustCompile(`\s+`)
var trimNonAlnumEdges = regexp.MustCompile(`(^[^a-zа-я0-9]+)|([^a-zа-я0-9]+$)`)

func buildExerciseSearchTerms(query string) []string {
	normalized := normalizeExerciseSearchInput(query)
	if normalized == "" {
		return []string{}
	}

	terms := make([]string, 0, 24)
	seen := make(map[string]struct{}, 24)
	add := func(term string) {
		term = normalizeExerciseSearchInput(term)
		if term == "" {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}

	add(normalized)
	for _, token := range strings.Split(normalized, " ") {
		add(token)
	}

	semanticMap := map[string][]string{
		"рука":              {"arm", "arms", "biceps", "triceps", "forearm", "forearms", "shoulders"},
		"руки":              {"arm", "arms", "biceps", "triceps", "forearm", "forearms", "shoulders"},
		"рук":               {"arm", "arms", "biceps", "triceps", "forearm", "forearms", "shoulders"},
		"бицепс":            {"biceps"},
		"трицепс":           {"triceps"},
		"предплеч":          {"forearm", "forearms"},
		"плеч":              {"shoulder", "shoulders", "front_delts", "side_delts", "rear_delts", "delts"},
		"дельт":             {"delts", "front_delts", "side_delts", "rear_delts", "shoulders"},
		"груд":              {"chest", "upper_chest", "lower_chest", "pecs"},
		"спин":              {"back", "lats", "traps", "rear_delts"},
		"пресс":             {"core", "abs"},
		"кор":               {"core", "abs"},
		"ягодиц":            {"glutes", "glute", "glute_medius", "glute_maximus"},
		"ягодич":            {"glutes", "glute", "glute_medius", "glute_maximus"},
		"ягодичк":           {"glutes", "glute", "glute_medius", "glute_maximus"},
		"малая ягодичная":   {"glute_medius", "glutes"},
		"средняя ягодичная": {"glute_medius", "glutes"},
		"большая ягодичная": {"glute_maximus", "glutes"},
		"ног":               {"legs", "quads", "hamstrings", "calves", "adductors", "abductors", "glutes"},
		"квадрицепс":        {"quads"},
		"бицепс бедра":      {"hamstrings"},
		"икр":               {"calves"},
	}

	for phrase, mapped := range semanticMap {
		if strings.Contains(normalized, phrase) {
			for _, item := range mapped {
				add(item)
				add(strings.ReplaceAll(item, "_", " "))
			}
		}
	}

	return terms
}

func normalizeExerciseSearchInput(input string) string {
	if input == "" {
		return ""
	}

	lower := strings.ToLower(input)
	lower = strings.ReplaceAll(lower, "ё", "е")
	lower = strings.ReplaceAll(lower, "элипс", "эллипс")
	lower = strings.ReplaceAll(lower, "элипсо", "эллипсо")
	lower = strings.ReplaceAll(lower, "элиптич", "эллиптич")
	lower = strings.ReplaceAll(lower, "элип", "эллип")

	var b strings.Builder
	b.Grow(len(lower))
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'а' && r <= 'я':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '/':
			b.WriteRune(' ')
		default:
			b.WriteRune(' ')
		}
	}

	normalized := normalizeWhitespace.ReplaceAllString(b.String(), " ")
	normalized = strings.TrimSpace(normalized)
	normalized = trimNonAlnumEdges.ReplaceAllString(normalized, "")
	return strings.TrimSpace(normalized)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Exercise, error) {
	var e Exercise
	err := r.db.QueryRow(ctx,
		`SELECT id,
		        COALESCE(NULLIF(name_ru, ''), NULLIF(name_en, ''), name) AS display_name,
		        COALESCE(NULLIF(name_en, ''), name) AS name_en,
		        type, muscle_group,
		        COALESCE(category, '') AS category,
		        COALESCE(body_region, '') AS body_region,
		        COALESCE(tracking_type, '') AS tracking_type,
		        COALESCE(difficulty, '') AS difficulty,
		        COALESCE(equipment, '{}'::text[]) AS equipment,
		        COALESCE(is_cardio, false) AS is_cardio,
		        is_default, created_at
		 FROM exercises WHERE id = $1`,
		id,
	).Scan(
		&e.ID, &e.Name, &e.NameEN, &e.Type, &e.MuscleGroup,
		&e.Category, &e.BodyRegion, &e.TrackingType, &e.Difficulty, &e.Equipment, &e.IsCardio,
		&e.IsDefault, &e.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("exercise get by id: %w", err)
	}
	return &e, nil
}

func (r *Repository) Create(ctx context.Context, e *Exercise) error {
	e.NameEN = strings.TrimSpace(e.Name)

	code := normalizeExerciseCode(e.Name)
	if code == "" {
		return fmt.Errorf("exercise create: name is required")
	}

	err := r.db.QueryRow(ctx,
		`INSERT INTO exercises (
		    exercise_code, name, name_en, name_ru,
		    type, muscle_group, is_default, source, updated_at
		)
		 VALUES ($1, $2, $3, $4, $5, $6, false, 'manual', NOW())
		 RETURNING id, created_at`,
		code, code, strings.TrimSpace(e.Name), strings.TrimSpace(e.Name), e.Type, e.MuscleGroup,
	).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("exercise create: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func normalizeExerciseCode(name string) string {
	trimmed := strings.TrimSpace(strings.ToLower(name))
	if trimmed == "" {
		return ""
	}

	replacer := strings.NewReplacer(" ", "_", "-", "_", "/", "_")
	raw := replacer.Replace(trimmed)
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '_'
	})
	return strings.Join(parts, "_")
}
