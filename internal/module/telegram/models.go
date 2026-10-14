package telegram

import (
	"encoding/json"
	"time"
)

// Update status lifecycle constants
const (
	UpdateStatusReceived   = "received"
	UpdateStatusProcessing = "processing"
	UpdateStatusCompleted  = "completed"
	UpdateStatusFailed     = "failed"
	UpdateStatusSkipped    = "skipped"
)

// Delivery status lifecycle constants (Durable Telegram Outbox)
const (
	DeliveryStatusPending = "pending"
	DeliveryStatusSending = "sending"
	DeliveryStatusSent    = "sent"
	DeliveryStatusFailed  = "failed"
)

// Telegram Chat types
const (
	ChatTypePrivate    = "private"
	ChatTypeGroup      = "group"
	ChatTypeSupergroup = "supergroup"
	ChatTypeChannel    = "channel"
)

// Message direction constants
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// Workout session status constants
const (
	SessionStatusProposed   = "proposed"
	SessionStatusAccepted   = "accepted"
	SessionStatusInProgress = "in_progress"
	SessionStatusCompleted  = "completed"
	SessionStatusAbandoned  = "abandoned"
	SessionStatusSuperseded = "superseded"
)

// Memory category constants
const (
	MemoryCategoryPhysicalLimitation = "physical_limitation"
	MemoryCategoryEquipment          = "equipment"
	MemoryCategoryPreference         = "preference"
	MemoryCategoryTransientState     = "transient_state"
)

// Memory scope constants
const (
	MemoryScopePermanent   = "permanent"
	MemoryScopeConditional = "conditional"
	MemoryScopeTransient   = "transient"
)

// Feedback type constants
const (
	FeedbackTypeTooHeavy            = "too_heavy"
	FeedbackTypeTooEasy             = "too_easy"
	FeedbackTypeJointPain           = "joint_pain"
	FeedbackTypeFormFatigue         = "form_fatigue"
	FeedbackTypeEquipmentPreference = "equipment_preference"
)

type TelegramUser struct {
	TelegramUserID int64     `json:"telegram_user_id"`
	UserID         string    `json:"user_id"`
	Username       *string   `json:"username,omitempty"`
	FirstName      *string   `json:"first_name,omitempty"`
	LastName       *string   `json:"last_name,omitempty"`
	LanguageCode   string    `json:"language_code"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type TelegramUpdate struct {
	UpdateID        int64            `json:"update_id"`
	CorrelationID   string           `json:"correlation_id,omitempty"`
	ChatID          int64            `json:"chat_id"`
	TelegramUserID  int64            `json:"telegram_user_id"`
	Status          string           `json:"status"`
	Attempts        int              `json:"attempts"`
	LockedAt        *time.Time       `json:"locked_at,omitempty"`
	LockedBy        *string          `json:"locked_by,omitempty"`
	RawPayload      json.RawMessage  `json:"raw_payload"`
	ResponsePayload *json.RawMessage `json:"response_payload,omitempty"`
	LastError       *string          `json:"last_error,omitempty"`
	CompletedAt     *time.Time       `json:"completed_at,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
}

type DomainReference struct {
	Type  string         `json:"type"`           // "workout", "exercise", "workout_session", "pending_clarification"
	ID    string         `json:"id,omitempty"`   // UUID if applicable
	Name  string         `json:"name,omitempty"` // Canonical or colloquial name
	Kind  string         `json:"kind,omitempty"` // Sub-classification
	Extra map[string]any `json:"extra,omitempty"`
}

type TelegramMessage struct {
	ChatID           int64             `json:"chat_id"`
	MessageID        int64             `json:"message_id"`
	CorrelationID    string            `json:"correlation_id,omitempty"`
	TelegramUserID   *int64            `json:"telegram_user_id,omitempty"` // nil for outbound bot messages
	UserID           string            `json:"user_id"`
	Direction        string            `json:"direction"` // "inbound" | "outbound"
	ReplyToMessageID *int64            `json:"reply_to_message_id,omitempty"`
	Text             *string           `json:"text,omitempty"`
	RawPayload       json.RawMessage   `json:"raw_payload"`
	DomainReferences []DomainReference `json:"domain_references"`
	CreatedAt        time.Time         `json:"created_at"`
}

type PendingClarification struct {
	Type          string          `json:"type"` // e.g. "exercise_disambiguation"
	OriginalQuery string          `json:"original_query"`
	Candidates    []string        `json:"candidates,omitempty"`
	Context       json.RawMessage `json:"context,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

type ConversationState struct {
	UserID               string                `json:"user_id"`
	ChatID               int64                 `json:"chat_id"`
	ActiveSessionID      *string               `json:"active_session_id,omitempty"`
	FocusExerciseID      *string               `json:"focus_exercise_id,omitempty"`
	PendingClarification *PendingClarification `json:"pending_clarification,omitempty"`
	LastInteractionAt    time.Time             `json:"last_interaction_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

type WorkoutSession struct {
	ID               string          `json:"id"`
	UserID           string          `json:"user_id"`
	Status           string          `json:"status"` // "proposed", "accepted", "in_progress", "completed", "abandoned", "superseded"
	ProposalData     json.RawMessage `json:"proposal_data"`
	AppliedWorkoutID *string         `json:"applied_workout_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
}

type UserMemory struct {
	ID              string          `json:"id"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	UserID          string          `json:"user_id"`
	Category        string          `json:"category"` // "physical_limitation", "equipment", "preference", "transient_state"
	Scope           string          `json:"scope"`    // "permanent", "conditional", "transient"
	Key             string          `json:"key"`
	Value           json.RawMessage `json:"value"`
	Confidence      float64         `json:"confidence"`
	ValidUntil      *time.Time      `json:"valid_until,omitempty"`
	SupersededBy    *string         `json:"superseded_by,omitempty"`
	SourceUpdateID  *int64          `json:"source_update_id,omitempty"`
	SourceMessageID *int64          `json:"source_message_id,omitempty"`
	SourceText      string          `json:"source_text,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type UserFeedbackEvent struct {
	ID            string          `json:"id"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	UserID        string          `json:"user_id"`
	WorkoutID     *string         `json:"workout_id,omitempty"`
	ExerciseID    *string         `json:"exercise_id,omitempty"`
	FeedbackType  string          `json:"feedback_type"` // "too_heavy", "too_easy", "joint_pain", "form_fatigue", "equipment_preference"
	RawText       string          `json:"raw_text"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     time.Time       `json:"created_at"`
}

type TelegramDelivery struct {
	ID                string          `json:"id"`
	CorrelationID     string          `json:"correlation_id,omitempty"`
	UpdateID          *int64          `json:"update_id,omitempty"`
	ChatID            int64           `json:"chat_id"`
	UserID            string          `json:"user_id"`
	Status            string          `json:"status"` // "pending", "sending", "sent", "failed"
	Payload           json.RawMessage `json:"payload"`
	Attempts          int             `json:"attempts"`
	LockedAt          *time.Time      `json:"locked_at,omitempty"`
	LockedBy          *string         `json:"locked_by,omitempty"`
	LastError         *string         `json:"last_error,omitempty"`
	TelegramMessageID *int64          `json:"telegram_message_id,omitempty"`
	SentAt            *time.Time      `json:"sent_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}
