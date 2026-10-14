package workout

import "time"

// Set captures one set of an exercise; cardio fields, strength fields,
// assistance, and machine multi-component weights are all supported.
type Set struct {
	SetNumber     int      `json:"set_number"`
	Reps          *int     `json:"reps,omitempty"`
	WeightKg      *float64 `json:"weight_kg,omitempty"`
	BaseWeightKg  *float64 `json:"base_weight_kg,omitempty"`
	AddedWeightKg *float64 `json:"added_weight_kg,omitempty"`
	AssistanceKg  *float64 `json:"assistance_kg,omitempty"`
	RPE           *float64 `json:"rpe,omitempty"`
	Difficulty    string   `json:"difficulty,omitempty"` // "easy", "normal", "hard", "very_hard", "failure"
	TimeMin       *float64 `json:"time_min,omitempty"`
	DistanceKm    *float64 `json:"distance_km,omitempty"`
	RPM           *int     `json:"rpm,omitempty"`
	Resistance    *int     `json:"resistance,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}

// WorkoutExerciseInput is the create-request payload for one exercise in a workout.
// Provide either exercise_id (UUID) or exercise_name.
type WorkoutExerciseInput struct {
	ExerciseID   string `json:"exercise_id,omitempty"`
	ExerciseName string `json:"exercise_name,omitempty"`
	Sets         []Set  `json:"sets"`
	Notes        string `json:"notes,omitempty"`
	OrderIndex   int    `json:"order_index,omitempty"`
}

// WorkoutExercise is the full response shape, denormalised with exercise meta.
type WorkoutExercise struct {
	ID           string `json:"id"`
	ExerciseID   string `json:"exercise_id"`
	ExerciseName string `json:"exercise_name"`
	ExerciseType string `json:"exercise_type"`
	MuscleGroup  string `json:"muscle_group"`
	Sets         []Set  `json:"sets"`
	Notes        string `json:"notes,omitempty"`
	OrderIndex   int    `json:"order_index"`
}

type Workout struct {
	ID                string            `json:"id"`
	UserID            string            `json:"user_id"`
	StartedAt         time.Time         `json:"started_at"`
	DurationMin       int               `json:"duration_min"`
	WorkoutType       string            `json:"workout_type"`
	TotalVolume       float64           `json:"total_volume"`
	Exercises         []WorkoutExercise `json:"exercises"`
	Notes             *string           `json:"notes,omitempty"`
	Source            string            `json:"source"`
	TelegramUpdateID  *int64            `json:"telegram_update_id,omitempty"`
	TelegramMessageID *int64            `json:"telegram_message_id,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
}

// WorkoutDraft represents the unvalidated structured extraction from free-form natural language.
type WorkoutDraft struct {
	WorkoutType string                 `json:"workout_type,omitempty"`
	DurationMin *int                   `json:"duration_min,omitempty"`
	Notes       string                 `json:"notes,omitempty"`
	Exercises   []WorkoutDraftExercise `json:"exercises"`
}

type WorkoutDraftExercise struct {
	RawName     string            `json:"raw_name"`
	Sets        []WorkoutDraftSet `json:"sets"`
	Notes       string            `json:"notes,omitempty"`
	Difficulty  string            `json:"difficulty,omitempty"` // "easy", "normal", "hard", "very_hard", "failure"
	FeedbackRaw string            `json:"feedback_raw,omitempty"`
}

type WorkoutDraftSet struct {
	SetNumber     int      `json:"set_number"`
	Reps          *int     `json:"reps,omitempty"`
	WeightKg      *float64 `json:"weight_kg,omitempty"`
	BaseWeightKg  *float64 `json:"base_weight_kg,omitempty"`
	AddedWeightKg *float64 `json:"added_weight_kg,omitempty"`
	AssistanceKg  *float64 `json:"assistance_kg,omitempty"`
	RPE           *float64 `json:"rpe,omitempty"`
	TimeMin       *float64 `json:"time_min,omitempty"`
	DistanceKm    *float64 `json:"distance_km,omitempty"`
	Resistance    *int     `json:"resistance,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}
