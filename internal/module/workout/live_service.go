package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LiveService owns validated, incremental workout performance writes. A plan is
// never stored as performed work; only reported sets enter workout_exercises.
type LiveService struct {
	repo *Repository
}

func NewLiveService(db *pgxpool.Pool) *LiveService {
	return &LiveService{repo: NewRepository(db)}
}

func (s *LiveService) Get(ctx context.Context, userID string, workoutID *string) (*Workout, error) {
	if workoutID == nil {
		return nil, nil
	}
	return s.repo.GetByID(ctx, *workoutID, userID)
}

func (s *LiveService) History(ctx context.Context, userID string) ([]Workout, error) {
	return s.repo.List(ctx, userID, "", "")
}

// LogSetTx requires the owning session row to be locked by the caller. The
// provided exercise ID must already be validated against the active proposal.
func (s *LiveService) LogSetTx(ctx context.Context, tx pgx.Tx, userID, sessionID, exerciseID string, orderIndex int, reported Set, updateID, messageID int64) (string, error) {
	if reported.SetNumber < 1 || reported.SetNumber > 100 || reported.Reps != nil && (*reported.Reps < 1 || *reported.Reps > 200) {
		return "", fmt.Errorf("invalid performed set")
	}
	for _, v := range []*float64{reported.WeightKg, reported.BaseWeightKg, reported.AddedWeightKg, reported.AssistanceKg, reported.TimeMin, reported.DistanceKm} {
		if v != nil && (*v < 0 || *v > 1000) {
			return "", fmt.Errorf("invalid performed metric")
		}
	}
	if reported.Reps == nil && reported.TimeMin == nil && reported.DistanceKm == nil {
		return "", fmt.Errorf("performed set has no repetitions, time, or distance")
	}
	var workoutID *string
	if err := tx.QueryRow(ctx, `SELECT applied_workout_id FROM workout_sessions WHERE id=$1 AND user_id=$2 AND status='in_progress' FOR UPDATE`, sessionID, userID).Scan(&workoutID); err != nil {
		return "", fmt.Errorf("lock active session: %w", err)
	}
	if workoutID == nil {
		w := &Workout{UserID: userID, StartedAt: time.Now(), WorkoutType: "strength", Source: "telegram", TelegramUpdateID: &updateID, TelegramMessageID: &messageID}
		if err := s.repo.CreateTx(ctx, tx, w, nil); err != nil {
			return "", err
		}
		workoutID = &w.ID
		if _, err := tx.Exec(ctx, `UPDATE workout_sessions SET applied_workout_id=$1, updated_at=NOW() WHERE id=$2`, w.ID, sessionID); err != nil {
			return "", err
		}
	}
	return s.appendSet(ctx, tx, *workoutID, exerciseID, orderIndex, reported, updateID, messageID)
}

func (s *LiveService) appendSet(ctx context.Context, tx pgx.Tx, workoutID, exerciseID string, orderIndex int, reported Set, updateID, messageID int64) (string, error) {
	var entryID string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id, sets_data FROM workout_exercises WHERE workout_id=$1 AND exercise_id=$2 FOR UPDATE`, workoutID, exerciseID).Scan(&entryID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		if reported.SetNumber != 1 {
			return "", fmt.Errorf("first performed set must be set 1")
		}
		b, _ := json.Marshal([]Set{reported})
		if err := tx.QueryRow(ctx, `INSERT INTO workout_exercises(workout_id, exercise_id, order_index, sets_data) VALUES($1,$2,$3,$4) RETURNING id`, workoutID, exerciseID, orderIndex, b).Scan(&entryID); err != nil {
			return "", fmt.Errorf("create performed exercise: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("load performed exercise: %w", err)
	} else {
		var sets []Set
		if err := json.Unmarshal(raw, &sets); err != nil {
			return "", err
		}
		if reported.SetNumber != len(sets)+1 {
			return "", fmt.Errorf("set %d cannot follow %d performed sets", reported.SetNumber, len(sets))
		}
		sets = append(sets, reported)
		b, _ := json.Marshal(sets)
		if _, err := tx.Exec(ctx, `UPDATE workout_exercises SET sets_data=$1 WHERE id=$2`, b, entryID); err != nil {
			return "", err
		}
	}
	if err := s.recordMutation(ctx, tx, workoutID, entryID, updateID, messageID, "sets_appended", fmt.Sprintf("set %d", reported.SetNumber)); err != nil {
		return "", err
	}
	if reported.Reps != nil {
		load := 0.0
		if reported.WeightKg != nil {
			load = *reported.WeightKg
		}
		if reported.BaseWeightKg != nil {
			load += *reported.BaseWeightKg
		}
		if reported.AddedWeightKg != nil {
			load += *reported.AddedWeightKg
		}
		if _, err := tx.Exec(ctx, `UPDATE workouts SET total_volume=total_volume+$1 WHERE id=$2`, float64(*reported.Reps)*load, workoutID); err != nil {
			return "", err
		}
	}
	if reported.Difficulty != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO user_feedback_events(user_id, workout_id, exercise_id, feedback_type, raw_text) SELECT user_id,$1,$2,'effort_comment',$3 FROM workouts WHERE id=$1`, workoutID, exerciseID, reported.Difficulty); err != nil {
			return "", err
		}
	}
	return workoutID, nil
}

func (s *LiveService) CorrectLastSetTx(ctx context.Context, tx pgx.Tx, userID, workoutID, exerciseID string, replacement Set, updateID, messageID int64) error {
	var entryID string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT we.id,we.sets_data FROM workout_exercises we JOIN workouts w ON w.id=we.workout_id WHERE w.id=$1 AND w.user_id=$2 AND we.exercise_id=$3 FOR UPDATE`, workoutID, userID, exerciseID).Scan(&entryID, &raw); err != nil {
		return err
	}
	var sets []Set
	if err := json.Unmarshal(raw, &sets); err != nil {
		return err
	}
	if len(sets) == 0 {
		return fmt.Errorf("no set to correct")
	}
	old := sets[len(sets)-1]
	beforeSet := old
	var beforeVolume float64
	if err := tx.QueryRow(ctx, `SELECT total_volume FROM workouts WHERE id=$1 AND user_id=$2 FOR UPDATE`, workoutID, userID).Scan(&beforeVolume); err != nil {
		return err
	}
	if replacement.Reps != nil {
		old.Reps = replacement.Reps
	}
	if replacement.WeightKg != nil {
		old.WeightKg = replacement.WeightKg
	}
	if replacement.AssistanceKg != nil {
		old.AssistanceKg = replacement.AssistanceKg
	}
	if replacement.Difficulty != "" {
		old.Difficulty = replacement.Difficulty
	}
	sets[len(sets)-1] = old
	b, _ := json.Marshal(sets)
	if _, err := tx.Exec(ctx, `UPDATE workout_exercises SET sets_data=$1 WHERE id=$2`, b, entryID); err != nil {
		return err
	}
	if err := s.recordMutation(ctx, tx, workoutID, entryID, updateID, messageID, "corrected", fmt.Sprintf("set %d", old.SetNumber)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE workouts SET total_volume=(SELECT COALESCE(SUM(COALESCE((j->>'reps')::numeric,0)*(COALESCE((j->>'weight_kg')::numeric,0)+COALESCE((j->>'base_weight_kg')::numeric,0)+COALESCE((j->>'added_weight_kg')::numeric,0))),0) FROM workout_exercises we CROSS JOIN LATERAL jsonb_array_elements(we.sets_data) j WHERE we.workout_id=$1) WHERE id=$1`, workoutID)
	if err != nil {
		return err
	}
	var afterVolume float64
	if err := tx.QueryRow(ctx, `SELECT total_volume FROM workouts WHERE id=$1`, workoutID).Scan(&afterVolume); err != nil {
		return err
	}
	changes, _ := json.Marshal(map[string]any{"before": beforeSet, "after": old})
	_, err = tx.Exec(ctx, `INSERT INTO workout_corrections(user_id,workout_id,workout_exercise_id,telegram_update_id,telegram_message_id,changes,before_volume,after_volume) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, userID, workoutID, entryID, updateID, messageID, changes, beforeVolume, afterVolume)
	return err
}

func (s *LiveService) recordMutation(ctx context.Context, tx pgx.Tx, workoutID, entryID string, updateID, messageID int64, operation, summary string) error {
	_, err := tx.Exec(ctx, `INSERT INTO workout_ingestion_increments(workout_id,workout_exercise_id,telegram_update_id,telegram_message_id,operation,summary) VALUES($1,$2,$3,$4,$5,$6)`, workoutID, entryID, updateID, messageID, operation, summary)
	return err
}
