package workout

import (
	"encoding/json"
	"time"

	"jasmine/internal/module/telegram"
)

type FieldChange struct {
	Field       string   `json:"field"`                  // "weight_kg", "base_weight_kg", "added_weight_kg", "assistance_kg", "reps", "set_count", "rpe", "difficulty", "notes"
	OldValue    *float64 `json:"old_value,omitempty"`    // e.g. 23.0
	NewValue    *float64 `json:"new_value,omitempty"`    // e.g. 27.0
	OldText     string   `json:"old_text,omitempty"`
	NewText     string   `json:"new_text,omitempty"`
	TargetSet   *int     `json:"target_set,omitempty"`   // 1-based index (e.g. 2 for "во втором")
	SetSelector string   `json:"set_selector,omitempty"` // "all", "first", "last", "specific"
}

type CorrectionDraft struct {
	IsCorrection     bool          `json:"is_correction"` // false for questions, hypotheticals, suggestions
	ExerciseRawName  string        `json:"exercise_raw_name,omitempty"`
	Changes          []FieldChange `json:"changes"`
	FeedbackAttached string        `json:"feedback_attached,omitempty"`
	Confidence       float64       `json:"confidence"`
}

type ResolvedTarget struct {
	WorkoutID         string
	WorkoutExerciseID string
	ExerciseID        string
	ExerciseName      string
	ResolutionSource  string // "reply_context" | "conversational_focus" | "explicit_token" | "recent_fallback"
	IsAmbiguous       bool
	AmbiguousPrompt   string
	CandidateOptions  []string
}

type CorrectionResult struct {
	Success          bool
	NeedsClarify     bool
	ClarificationMsg string
	ConfirmationHTML string
	WorkoutID        string
	ExerciseID       string
	DomainReferences []telegram.DomainReference
}

type WorkoutCorrectionAudit struct {
	WorkoutID         string          `json:"workout_id"`
	WorkoutExerciseID string          `json:"workout_exercise_id,omitempty"`
	TelegramUpdateID  *int64          `json:"telegram_update_id,omitempty"`
	Changes           []FieldChange   `json:"changes"`
	BeforeVolume      float64         `json:"before_volume"`
	AfterVolume       float64         `json:"after_volume"`
	CreatedAt         time.Time       `json:"created_at"`
	RawPayload        json.RawMessage `json:"raw_payload,omitempty"`
}
