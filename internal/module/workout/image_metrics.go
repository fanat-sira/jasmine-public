package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

var ErrMachineUnresolved = errors.New("machine name needs clarification")

// MachineMetrics contains validated observations and a user-resolved machine name.
type MachineMetrics struct {
	MachineName     string
	At              time.Time
	Historical      bool
	DurationSeconds *int
	DistanceKM      *float64
	Calories        *float64
	HeartRateBPM    *int
	SpeedKMH        *float64
	Resistance      *int
	Level           *int
	InclinePercent  *float64
	Watts           *float64
	RPM             *int
	Steps           *int
	Floors          *int
	Strokes         *int
	Repetitions     *int
	LoadKG          *float64
	FileUniqueID    string
	SourceUpdateID  int64
	SourceMessageID int64
}

func (p *IngestionPipeline) ApplyMachineMetricsTx(ctx context.Context, tx pgx.Tx, userID string, m MachineMetrics) (string, error) {
	if p == nil || p.exerciseRes == nil || p.workoutRepo == nil {
		return "", fmt.Errorf("workout ingestion unavailable")
	}
	resolution, err := p.exerciseRes.Resolve(ctx, m.MachineName)
	if err != nil {
		return "", err
	}
	if resolution == nil || resolution.Exercise == nil || resolution.Status == exercise.ResolutionAmbiguous {
		return "", ErrMachineUnresolved
	}
	ex := resolution.Exercise
	set := Set{SetNumber: 1, DistanceKm: m.DistanceKM, RPM: m.RPM, Resistance: m.Resistance, Reps: m.Repetitions, WeightKg: m.LoadKG, Notes: fmt.Sprintf("Экран тренажёра: %s", m.FileUniqueID)}
	if m.DurationSeconds != nil {
		v := float64(*m.DurationSeconds) / 60
		set.TimeMin = &v
	}
	if m.HeartRateBPM != nil {
		set.Notes += fmt.Sprintf("; пульс %d", *m.HeartRateBPM)
	}
	if m.Calories != nil {
		set.Notes += fmt.Sprintf("; %.0f ккал", *m.Calories)
	}
	if m.SpeedKMH != nil {
		set.Notes += fmt.Sprintf("; %.1f км/ч", *m.SpeedKMH)
	}
	if m.Level != nil {
		set.Notes += fmt.Sprintf("; уровень %d", *m.Level)
	}
	if m.InclinePercent != nil {
		set.Notes += fmt.Sprintf("; уклон %.1f%%", *m.InclinePercent)
	}
	if m.Watts != nil {
		set.Notes += fmt.Sprintf("; %.0f Вт", *m.Watts)
	}
	if m.Steps != nil {
		set.Notes += fmt.Sprintf("; %d шагов", *m.Steps)
	}
	if m.Floors != nil {
		set.Notes += fmt.Sprintf("; %d этажей", *m.Floors)
	}
	if m.Strokes != nil {
		set.Notes += fmt.Sprintf("; %d гребков", *m.Strokes)
	}

	var session *telegram.WorkoutSession
	if !m.Historical && p.telegramRepo != nil {
		session, err = p.telegramRepo.GetActiveWorkoutSessionForUpdate(ctx, tx, userID)
		if err != nil && err != telegram.ErrNotFound {
			return "", err
		}
	}
	workoutID := ""
	if session != nil && session.AppliedWorkoutID != nil {
		workoutID = *session.AppliedWorkoutID
	}
	if workoutID == "" && m.Historical {
		err = tx.QueryRow(ctx, `SELECT id FROM workouts WHERE user_id=$1 AND (started_at AT TIME ZONE $2)::date=$3::date ORDER BY started_at DESC LIMIT 1 FOR UPDATE`, userID, m.At.Location().String(), m.At.Format("2006-01-02")).Scan(&workoutID)
		if err != nil && err != pgx.ErrNoRows {
			return "", err
		}
	}
	duration := 0
	if m.DurationSeconds != nil {
		duration = (*m.DurationSeconds + 30) / 60
	}
	if workoutID == "" {
		workoutType := ex.Type
		if workoutType == "" {
			workoutType = "cardio"
		}
		w := &Workout{UserID: userID, StartedAt: m.At, DurationMin: 0, WorkoutType: workoutType, Source: "telegram", TelegramUpdateID: &m.SourceUpdateID, TelegramMessageID: &m.SourceMessageID}
		if err = p.workoutRepo.CreateTx(ctx, tx, w, nil); err != nil {
			return "", err
		}
		workoutID = w.ID
		if session != nil && p.telegramRepo != nil {
			if err = p.telegramRepo.UpdateWorkoutSessionStatusTx(ctx, tx, session.ID, userID, "in_progress", &workoutID); err != nil {
				return "", err
			}
		}
	}
	var existingID string
	var oldJSON []byte
	err = tx.QueryRow(ctx, `SELECT id,sets_data FROM workout_exercises WHERE workout_id=$1 AND exercise_id=$2 LIMIT 1 FOR UPDATE`, workoutID, ex.ID).Scan(&existingID, &oldJSON)
	if err != nil && err != pgx.ErrNoRows {
		return "", err
	}
	var entryID string
	if err == pgx.ErrNoRows {
		var pos int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM workout_exercises WHERE workout_id=$1`, workoutID).Scan(&pos); err != nil {
			return "", err
		}
		b, _ := json.Marshal([]Set{set})
		if err = tx.QueryRow(ctx, `INSERT INTO workout_exercises(workout_id,exercise_id,order_index,sets_data) VALUES ($1,$2,$3,$4) RETURNING id`, workoutID, ex.ID, pos, b).Scan(&entryID); err != nil {
			return "", err
		}
	} else {
		var sets []Set
		if err = json.Unmarshal(oldJSON, &sets); err != nil {
			return "", err
		}
		set.SetNumber = len(sets) + 1
		sets = append(sets, set)
		b, _ := json.Marshal(sets)
		if _, err = tx.Exec(ctx, `UPDATE workout_exercises SET sets_data=$1 WHERE id=$2`, b, existingID); err != nil {
			return "", err
		}
		entryID = existingID
	}
	volume := 0.0
	if m.Repetitions != nil && m.LoadKG != nil {
		volume = float64(*m.Repetitions) * (*m.LoadKG)
	}
	if _, err = tx.Exec(ctx, `UPDATE workouts SET duration_min=duration_min+$2,total_volume=total_volume+$4 WHERE id=$1 AND user_id=$3`, workoutID, duration, userID, volume); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workout_ingestion_increments(correlation_id,workout_id,workout_exercise_id,telegram_update_id,telegram_message_id,operation,summary) VALUES ($1,$2,$3,$4,$5,'image_machine_metrics',$6)`, correlation.GetCorrelationID(ctx), workoutID, entryID, m.SourceUpdateID, m.SourceMessageID, "Machine display: "+m.MachineName)
	return workoutID, err
}
