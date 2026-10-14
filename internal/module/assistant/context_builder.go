package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

type ContextBudgets struct {
	MaxTurns           int `json:"max_turns"`            // Default: 8
	MaxWorkouts        int `json:"max_workouts"`         // Default: 3
	MaxExerciseHistory int `json:"max_exercise_history"` // Default: 3
	MaxFeedbackEntries int `json:"max_feedback_entries"` // Default: 5
	MaxChars           int `json:"max_chars"`            // Default: 4000
}

func DefaultBudgets() ContextBudgets {
	return ContextBudgets{
		MaxTurns:           8,
		MaxWorkouts:        3,
		MaxExerciseHistory: 3,
		MaxFeedbackEntries: 5,
		MaxChars:           4000,
	}
}

type ProfileContext struct {
	UserID   string `json:"user_id"`
	Goal     string `json:"goal,omitempty"`
	Units    string `json:"units,omitempty"`
	Language string `json:"language,omitempty"`
}

type ConversationContext struct {
	ActionType              ActionType                     `json:"action_type"`
	Profile                 ProfileContext                 `json:"profile"`
	ActiveSession           *telegram.WorkoutSession       `json:"active_session,omitempty"`
	PendingClarification    *telegram.PendingClarification `json:"pending_clarification,omitempty"`
	RelevantTurns           []telegram.TelegramMessage     `json:"relevant_turns"`
	ActivePreferences       []string                       `json:"active_preferences"`
	ActiveConstraints       []string                       `json:"active_constraints"`
	TransientStates         []string                       `json:"transient_states"`
	RelevantExerciseHistory []workout.WorkoutExercise      `json:"relevant_exercise_history,omitempty"`
	RelevantFeedbackSummary map[string][]string            `json:"relevant_feedback_summary,omitempty"`
	FormattedPromptContext  string                         `json:"formatted_prompt_context"`
}

type ContextBuilder struct {
	budgets ContextBudgets
}

func NewContextBuilder(budgets ContextBudgets) *ContextBuilder {
	if budgets.MaxTurns <= 0 {
		budgets = DefaultBudgets()
	}
	return &ContextBuilder{budgets: budgets}
}

func (b *ContextBuilder) GetBudgets() ContextBudgets {
	return b.budgets
}

// BuildContext constructs a compact, prioritized, action-specific conversational context.
func (b *ContextBuilder) BuildContext(
	ctx context.Context,
	action ActionType,
	userID string,
	convState *telegram.ConversationState,
	allMemories []telegram.UserMemory,
	recentMessages []telegram.TelegramMessage,
	recentWorkouts []workout.Workout,
	feedbackEvents []telegram.UserFeedbackEvent,
	focusExerciseName string,
) *ConversationContext {
	c := &ConversationContext{
		ActionType: action,
		Profile: ProfileContext{
			UserID:   userID,
			Goal:     "Hypertrophy & Strength",
			Units:    "metric",
			Language: "ru",
		},
		ActivePreferences:       []string{},
		ActiveConstraints:       []string{},
		TransientStates:         []string{},
		RelevantFeedbackSummary: make(map[string][]string),
	}

	if convState != nil {
		c.PendingClarification = convState.PendingClarification
	}

	// 1. Process Layered Memories (Un-superseded and unexpired)
	now := time.Now()
	for _, m := range allMemories {
		if m.SupersededBy != nil {
			continue // Skip superseded historical memory
		}
		if m.ValidUntil != nil && m.ValidUntil.Before(now) {
			continue // Skip expired transient memory
		}

		var valStr string
		if len(m.Value) > 0 {
			var parsed string
			if err := json.Unmarshal(m.Value, &parsed); err == nil {
				valStr = parsed
			} else {
				valStr = string(m.Value)
			}
		}
		if valStr == "" {
			valStr = m.Key
		}

		switch m.Category {
		case "preference", "equipment":
			c.ActivePreferences = append(c.ActivePreferences, fmt.Sprintf("• %s", valStr))
		case "physical_limitation":
			c.ActiveConstraints = append(c.ActiveConstraints, fmt.Sprintf("• %s", valStr))
		case "transient_state":
			c.TransientStates = append(c.TransientStates, fmt.Sprintf("• %s", valStr))
		}
	}

	// 2. Select Relevant Conversation Turns (Prioritize references & clarifications over raw recency)
	c.RelevantTurns = b.selectRelevantTurns(recentMessages, convState, focusExerciseName)

	// 3. Select Relevant Exercise History & Feedback (Action-dependent)
	if action == ActionWorkoutLog || action == ActionWorkoutCorrection || action == ActionFitnessQuestion || action == ActionWorkoutRequest {
		limitWorkouts := b.budgets.MaxWorkouts
		if len(recentWorkouts) < limitWorkouts {
			limitWorkouts = len(recentWorkouts)
		}
		for i := 0; i < limitWorkouts; i++ {
			w := recentWorkouts[i]
			for _, we := range w.Exercises {
				if focusExerciseName == "" || strings.Contains(strings.ToLower(we.ExerciseName), strings.ToLower(focusExerciseName)) {
					c.RelevantExerciseHistory = append(c.RelevantExerciseHistory, we)
				}
			}
		}
	}

	// 4. Summarize Feedback Events by Exercise
	feedbackLimit := b.budgets.MaxFeedbackEntries
	count := 0
	for _, fb := range feedbackEvents {
		if count >= feedbackLimit {
			break
		}
		exName := "General"
		if fb.ExerciseID != nil {
			exName = *fb.ExerciseID
		}
		c.RelevantFeedbackSummary[exName] = append(c.RelevantFeedbackSummary[exName], fb.RawText)
		count++
	}

	// 5. Format Prompt Context string for LLM injection
	c.FormattedPromptContext = b.formatPrompt(c)
	return c
}

func (b *ContextBuilder) selectRelevantTurns(
	messages []telegram.TelegramMessage,
	state *telegram.ConversationState,
	focusName string,
) []telegram.TelegramMessage {
	if len(messages) == 0 {
		return []telegram.TelegramMessage{}
	}

	// Bound history by MaxTurns budget
	limit := b.budgets.MaxTurns
	if len(messages) > limit {
		return messages[len(messages)-limit:]
	}
	return messages
}

func (b *ContextBuilder) formatPrompt(c *ConversationContext) string {
	var sb strings.Builder

	if len(c.ActivePreferences) > 0 {
		sb.WriteString("User Preferences:\n" + strings.Join(c.ActivePreferences, "\n") + "\n\n")
	}
	if len(c.ActiveConstraints) > 0 {
		sb.WriteString("Physical Limitations & Constraints:\n" + strings.Join(c.ActiveConstraints, "\n") + "\n\n")
	}
	if len(c.TransientStates) > 0 {
		sb.WriteString("Temporary Context (Today/This week):\n" + strings.Join(c.TransientStates, "\n") + "\n\n")
	}
	if c.PendingClarification != nil {
		sb.WriteString(fmt.Sprintf("Pending Clarification: %s (Candidates: %s)\n\n",
			c.PendingClarification.OriginalQuery,
			strings.Join(c.PendingClarification.Candidates, ", "),
		))
	}

	return strings.TrimSpace(sb.String())
}
