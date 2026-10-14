package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("workout not found")
var ErrExerciseEntryNotFound = errors.New("workout exercise not found")

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ResolveNames looks up exercise IDs by name for any inputs that have ExerciseName set.
func (r *Repository) ResolveNames(ctx context.Context, inputs []WorkoutExerciseInput) error {
	for i, inp := range inputs {
		if inp.ExerciseID != "" {
			continue
		}
		if inp.ExerciseName == "" {
			return fmt.Errorf("exercise at index %d: provide exercise_id or exercise_name", i)
		}
		var id string
		err := r.db.QueryRow(ctx,
			`WITH search_input AS (
			    SELECT
			        trim($1)::text AS raw_query,
			        lower(trim($1)) AS q_lower,
			        regexp_replace(lower(trim($1)), '[^a-zа-я0-9]+', ' ', 'g') AS normalized
			)
			SELECT e.id
			FROM exercises e
			CROSS JOIN search_input si
			CROSS JOIN LATERAL (
			    SELECT
			        COALESCE(e.name_ru, '') || ' ' ||
			        COALESCE(e.name_en, '') || ' ' ||
			        COALESCE(e.name, '') || ' ' ||
			        COALESCE(e.exercise_code, '') || ' ' ||
			        array_to_string(COALESCE(e.aliases_ru, '{}'::text[]) || COALESCE(e.aliases_en, '{}'::text[]), ' ') AS searchable_text
			) txt
			WHERE
			    lower(e.exercise_code) = si.q_lower
			    OR lower(e.name) = si.q_lower
			    OR lower(e.name_ru) = si.q_lower
			    OR lower(e.name_en) = si.q_lower
			    OR txt.searchable_text ILIKE '%' || si.raw_query || '%'
			    OR similarity(lower(txt.searchable_text), si.q_lower) >= 0.22
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
			    e.is_default DESC
			LIMIT 1`,
			inp.ExerciseName,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("exercise %q not found", inp.ExerciseName)
		}
		inputs[i].ExerciseID = id
	}
	return nil
}

// Create inserts the workout row and all workout_exercises inside a transaction.
func (r *Repository) Create(ctx context.Context, w *Workout, inputs []WorkoutExerciseInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := r.CreateTx(ctx, tx, w, inputs); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// CreateTx inserts the workout row and all workout_exercises inside an existing transaction.
func (r *Repository) CreateTx(ctx context.Context, tx pgx.Tx, w *Workout, inputs []WorkoutExerciseInput) error {
	source := w.Source
	if source == "" {
		source = "manual"
	}

	err := tx.QueryRow(ctx,
		`INSERT INTO workouts (user_id, started_at, duration_min, workout_type, total_volume, notes, source, telegram_update_id, telegram_message_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		w.UserID, w.StartedAt, w.DurationMin, w.WorkoutType, w.TotalVolume, w.Notes, source, w.TelegramUpdateID, w.TelegramMessageID,
	).Scan(&w.ID, &w.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert workout: %w", err)
	}

	for i, inp := range inputs {
		setsJSON, err := json.Marshal(inp.Sets)
		if err != nil {
			return fmt.Errorf("marshal sets: %w", err)
		}
		orderIndex := inp.OrderIndex
		if orderIndex == 0 {
			orderIndex = i
		}
		var weID string
		if err = tx.QueryRow(ctx,
			`INSERT INTO workout_exercises (workout_id, exercise_id, order_index, notes, sets_data)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id`,
			w.ID, inp.ExerciseID, orderIndex, nullStr(inp.Notes), setsJSON,
		).Scan(&weID); err != nil {
			return fmt.Errorf("insert workout_exercise %d: %w", i, err)
		}
	}

	return nil
}

// fetchExercises returns WorkoutExercise rows grouped by workout ID, ordered by order_index.
func (r *Repository) fetchExercises(ctx context.Context, workoutIDs []string) (map[string][]WorkoutExercise, error) {
	if len(workoutIDs) == 0 {
		return map[string][]WorkoutExercise{}, nil
	}

	rows, err := r.db.Query(ctx,
		`SELECT we.id, we.workout_id, we.exercise_id,
		        COALESCE(NULLIF(e.name_ru, ''), NULLIF(e.name_en, ''), e.name) AS display_name,
		        e.type, e.muscle_group,
		        we.sets_data, COALESCE(we.notes, ''), we.order_index
		 FROM workout_exercises we
		 JOIN exercises e ON e.id = we.exercise_id
		 WHERE we.workout_id = ANY($1::uuid[])
		 ORDER BY we.order_index ASC`,
		workoutIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch exercises: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]WorkoutExercise)
	for rows.Next() {
		var we WorkoutExercise
		var workoutID string
		var setsJSON []byte
		if err := rows.Scan(
			&we.ID, &workoutID, &we.ExerciseID,
			&we.ExerciseName, &we.ExerciseType, &we.MuscleGroup,
			&setsJSON, &we.Notes, &we.OrderIndex,
		); err != nil {
			return nil, fmt.Errorf("scan workout_exercise: %w", err)
		}
		if err := json.Unmarshal(setsJSON, &we.Sets); err != nil {
			return nil, fmt.Errorf("unmarshal sets: %w", err)
		}
		result[workoutID] = append(result[workoutID], we)
	}
	return result, rows.Err()
}

func (r *Repository) GetByID(ctx context.Context, id, userID string) (*Workout, error) {
	w := &Workout{}
	err := r.db.QueryRow(ctx,
		`SELECT id, user_id, started_at, duration_min, workout_type, total_volume, notes, COALESCE(source, 'manual'), telegram_update_id, telegram_message_id, created_at
		 FROM workouts WHERE id = $1 AND user_id = $2`,
		id, userID,
	).Scan(&w.ID, &w.UserID, &w.StartedAt, &w.DurationMin, &w.WorkoutType, &w.TotalVolume, &w.Notes, &w.Source, &w.TelegramUpdateID, &w.TelegramMessageID, &w.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("workout get by id: %w", err)
	}

	exMap, err := r.fetchExercises(ctx, []string{w.ID})
	if err != nil {
		return nil, err
	}
	w.Exercises = exMap[w.ID]
	if w.Exercises == nil {
		w.Exercises = []WorkoutExercise{}
	}
	return w, nil
}

func (r *Repository) List(ctx context.Context, userID, from, to string) ([]Workout, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, user_id, started_at, duration_min, workout_type, total_volume, notes, COALESCE(source, 'manual'), telegram_update_id, telegram_message_id, created_at
		 FROM workouts
		 WHERE user_id = $1
		   AND ($2::date IS NULL OR started_at >= $2::date)
		   AND ($3::date IS NULL OR started_at < ($3::date + INTERVAL '1 day'))
		 ORDER BY started_at DESC
		 LIMIT 100`,
		userID, nullStr(from), nullStr(to),
	)
	if err != nil {
		return nil, fmt.Errorf("workout list: %w", err)
	}
	defer rows.Close()

	var out []Workout
	var ids []string
	for rows.Next() {
		w := Workout{}
		if err := rows.Scan(
			&w.ID, &w.UserID, &w.StartedAt, &w.DurationMin,
			&w.WorkoutType, &w.TotalVolume, &w.Notes, &w.Source, &w.TelegramUpdateID, &w.TelegramMessageID, &w.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("workout list scan: %w", err)
		}
		out = append(out, w)
		ids = append(ids, w.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		exMap, err := r.fetchExercises(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range out {
			out[i].Exercises = exMap[out[i].ID]
			if out[i].Exercises == nil {
				out[i].Exercises = []WorkoutExercise{}
			}
		}
	}
	if out == nil {
		out = []Workout{}
	}
	return out, nil
}

func (r *Repository) Delete(ctx context.Context, id, userID string) error {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM workouts WHERE id = $1 AND user_id = $2`, id, userID,
	)
	if err != nil {
		return fmt.Errorf("workout delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) DeleteExerciseEntry(ctx context.Context, entryID, userID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var workoutID string
	err = tx.QueryRow(ctx,
		`DELETE FROM workout_exercises we
		 USING workouts w
		 WHERE we.id = $1
		   AND we.workout_id = w.id
		   AND w.user_id = $2
		 RETURNING we.workout_id`,
		entryID, userID,
	).Scan(&workoutID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExerciseEntryNotFound
	}
	if err != nil {
		return fmt.Errorf("delete workout exercise: %w", err)
	}

	_, err = tx.Exec(ctx,
		`UPDATE workouts w
		 SET total_volume = COALESCE((
		     SELECT SUM(
		         COALESCE((s->>'reps')::numeric, 0) *
		         COALESCE((s->>'weight_kg')::numeric, 0)
		     )
		     FROM workout_exercises we
		     CROSS JOIN LATERAL jsonb_array_elements(we.sets_data) AS s
		     WHERE we.workout_id = w.id
		 ), 0)
		 WHERE w.id = $1`,
		workoutID,
	)
	if err != nil {
		return fmt.Errorf("recalculate workout volume: %w", err)
	}

	return tx.Commit(ctx)
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
