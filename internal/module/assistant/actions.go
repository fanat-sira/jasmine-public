package assistant

import (
	"encoding/json"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

type ActionType string

const (
	ActionWorkoutLog        ActionType = "workout_log"
	ActionWorkoutCorrection ActionType = "workout_correction"
	ActionFeedback          ActionType = "feedback"
	ActionMemoryUpdate      ActionType = "memory_update"
	ActionProfileUpdate     ActionType = "profile_update"
	ActionHistoryQuery      ActionType = "history_query"
	ActionProgressQuery     ActionType = "progress_query"
	ActionWorkoutRequest    ActionType = "workout_request"
	ActionFitnessQuestion   ActionType = "fitness_question"
	ActionOther             ActionType = "other"
)

type ActionItem struct {
	Type        ActionType      `json:"type"`
	Confidence  float64         `json:"confidence"`
	PayloadJSON json.RawMessage `json:"payload,omitempty"`
}

type InterpretedMessage struct {
	RawText          string                   `json:"raw_text"`
	Actions          []ActionItem             `json:"actions"`
	WorkoutDraft     *workout.WorkoutDraft    `json:"workout_draft,omitempty"`
	CorrectionDraft  *workout.CorrectionDraft `json:"correction_draft,omitempty"`
	MemoryCandidates []MemoryCandidate        `json:"memory_candidates,omitempty"`
	FeedbackText     string                   `json:"feedback_text,omitempty"`
	IsDeferred       bool                     `json:"is_deferred"`
	DeferredReason   string                   `json:"deferred_reason,omitempty"`
}

type OrchestratorResponse struct {
	Success          bool
	NeedsClarify     bool
	ClarificationMsg string
	ReplyHTML        string
	DomainReferences []telegram.DomainReference
	ActionsExecuted  []ActionType
}

type MemoryCandidate struct {
	Category       string     `json:"category"`             // "preference", "equipment", "physical_limitation", "transient_state"
	Intent         string     `json:"intent,omitempty"`     // e.g. "avoid_exercise"
	TargetRaw      string     `json:"target_raw,omitempty"` // e.g. "гравитон", "лег пресс"
	Key            string     `json:"key"`
	Value          string     `json:"value"`
	Scope          string     `json:"scope"` // "permanent", "conditional", "transient"
	Confidence     float64    `json:"confidence"`
	ValidUntilHint string     `json:"valid_until_hint,omitempty"` // "today", "this_week", "now", etc.
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
	IsReversal     bool       `json:"is_reversal"` // e.g. "дорожку снова можно" / "забудь что не люблю X"
	SourceText     string     `json:"source_text"`
}
