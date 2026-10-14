package assistant

import (
	"jasmine/internal/module/prompts"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"jasmine/internal/pkg/correlation"
)

// CoachToolCall contains user observations, never trusted database identifiers.
type CoachToolCall struct {
	Name         string   `json:"name"`
	Exercise     string   `json:"exercise,omitempty"`
	Reps         *int     `json:"reps,omitempty"`
	SetNumber    *int     `json:"set_number,omitempty"`
	WeightKg     *float64 `json:"weight_kg,omitempty"`
	AssistanceKg *float64 `json:"assistance_kg,omitempty"`
	Difficulty   string   `json:"difficulty,omitempty"`
	DurationMin  *int     `json:"duration_min,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

type CoachPlan struct {
	Calls []CoachToolCall `json:"calls"`
}
type CoachTurn struct {
	Text             string `json:"text"`
	ReplyToMessageID *int64 `json:"reply_to_message_id,omitempty"`
}
type CoachRecentMessage struct {
	MessageID int64  `json:"message_id"`
	Direction string `json:"direction"`
	Text      string `json:"text"`
}

type CoachContext struct {
	UserID               string                         `json:"user_id"`
	TurnText             string                         `json:"turn_text,omitempty"`
	Goal                 string                         `json:"goal,omitempty"`
	ProfileData          json.RawMessage                `json:"profile,omitempty"`
	Timezone             string                         `json:"timezone,omitempty"`
	Session              *telegram.WorkoutSession       `json:"session,omitempty"`
	Proposal             *WorkoutProposalDraft          `json:"proposal,omitempty"`
	Performed            *workout.Workout               `json:"performed,omitempty"`
	Current              *ProposedExerciseDraft         `json:"current,omitempty"`
	CurrentIndex         int                            `json:"current_index"`
	CurrentSet           int                            `json:"current_set"`
	LastSet              *workout.Set                   `json:"last_set,omitempty"`
	ReplyExerciseID      string                         `json:"reply_exercise_id,omitempty"`
	FocusExerciseID      string                         `json:"focus_exercise_id,omitempty"`
	Memories             []telegram.UserMemory          `json:"memories,omitempty"`
	History              []workout.Workout              `json:"history,omitempty"`
	RecentMessages       []CoachRecentMessage           `json:"-"`
	Observations         []telegram.DomainReference     `json:"observations,omitempty"`
	LatestImageFileID    string                         `json:"-"`
	LatestLabelDraft     *NutritionLabelDraft           `json:"-"`
	LatestLabelCaption   string                         `json:"-"`
	LatestLabelMessageID int64                          `json:"-"`
	LatestLabelReply     bool                           `json:"-"`
	PendingClarification *telegram.PendingClarification `json:"pending_clarification,omitempty"`
	ResponseIntent       string                         `json:"-"`
}

type CoachPlanner interface {
	Plan(context.Context, CoachTurn, *CoachContext) (*CoachPlan, error)
}
type modelCoachPlanner struct {
	client  *openrouter.Client
	model   string
	prompts prompts.Store
}

func (p modelCoachPlanner) Plan(ctx context.Context, turn CoachTurn, c *CoachContext) (*CoachPlan, error) {
	if p.client == nil {
		return ruleCoachPlan(turn, c), nil
	}
	b, _ := json.Marshal(struct {
		Turn    CoachTurn     `json:"turn"`
		Context *CoachContext `json:"context"`
	}{turn, c})
	aiCtx, cancel := context.WithTimeout(correlation.WithOperation(ctx, "coach_planner"), 45*time.Second)
	defer cancel()
	start := time.Now()
	systemPrompt, err := prompts.Resolve(ctx, p.prompts, prompts.KeyCoachPlanner)
	if err != nil {
		return nil, err
	}
	raw, err := p.client.SendConversation(aiCtx, p.model, systemPrompt, coachHistoryMessages(c), string(b))
	if err == nil {
		var plan CoachPlan
		err = json.Unmarshal([]byte(stripMarkdownFences(raw)), &plan)
		if err == nil && len(plan.Calls) <= 5 {
			p.client.RecordStructuredValidation(ctx, "coach_planner", p.model, time.Since(start).Milliseconds(), true, nil)
			return &plan, nil
		}
		p.client.RecordStructuredValidation(ctx, "coach_planner", p.model, time.Since(start).Milliseconds(), false, fmt.Errorf("invalid plan: %v", err))
	}
	return ruleCoachPlan(turn, c), nil
}

var coachNumber = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
var coachWeight = regexp.MustCompile(`на\s+(\d+(?:[.,]\d+)?)`)
var coachWeightUnit = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*(?:кг|kg)(?:\s|$)`)

func explicitCoachWeight(text string) *float64 {
	t := strings.ToLower(text)
	m := coachWeight.FindStringSubmatch(t)
	if len(m) < 2 {
		m = coachWeightUnit.FindStringSubmatch(t)
	}
	if len(m) < 2 {
		return nil
	}
	v, e := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	if e != nil {
		return nil
	}
	return &v
}

var coachDuration = regexp.MustCompile(`(\d{1,3})\s*мин`)

func looksLikeHistoricalCorrection(text string) bool {
	t := strings.ToLower(text)
	return (strings.Contains(t, "исправ") || strings.Contains(t, "не ") && strings.Contains(t, " а ")) && coachNumber.MatchString(t)
}
func ruleCoachPlan(turn CoachTurn, c *CoachContext) *CoachPlan {
	t := strings.ToLower(strings.TrimSpace(turn.Text))
	plan := &CoachPlan{}
	add := func(n string) { plan.Calls = append(plan.Calls, CoachToolCall{Name: n}) }
	if t == "" {
		return plan
	}
	if c.Session == nil {
		if strings.Contains(t, "запомни") || strings.Contains(t, "не люблю") || strings.Contains(t, "пока не давай") {
			add("save_memory")
		}
		if IsWorkoutGenerationRequest(t) || strings.Contains(t, "че сегодня делаем") || strings.Contains(t, "давай минут") {
			if strings.Contains(t, "убит") || strings.Contains(t, "болит") || strings.Contains(t, "устал") {
				add("save_transient_state")
			}
			add("propose_workout")
		} else if strings.Contains(t, "прогресс") || strings.Contains(t, "прошл") || strings.Contains(t, "истор") || strings.Contains(t, "что я делал") {
			if strings.Contains(t, "прогресс") {
				add("get_progression")
			} else {
				add("get_exercise_history")
			}
		}
		if len(plan.Calls) == 0 && !workout.IsPlausibleWorkoutLog(t) && !looksLikeHistoricalCorrection(t) {
			add("general_chat")
		}
		return plan
	}
	if c.Session.Status == "proposed" {
		if strings.Contains(t, "отмени") || t == "отмена" || strings.Contains(t, "не буду тренироваться") {
			add("abandon_workout")
			return plan
		}
		if strings.Contains(t, "добавь") {
			add("add_exercise")
			return plan
		}
		if strings.Contains(t, "убери") {
			add("skip_exercise")
			return plan
		}
		if strings.Contains(t, "замени") || strings.Contains(t, "не хочу") {
			add("replace_exercise")
			return plan
		}
		if t == "го" || t == "старт" || t == "погнали" || t == "начали" || t == "начинаем" || t == "поехали" || t == "начал" || t == "начала" || t == "начинаю" || strings.HasPrefix(t, "начинай") || strings.HasPrefix(t, "начинаем") {
			add("start_workout")
		} else if strings.Contains(t, "минут") || strings.Contains(t, "замени") {
			add("replan_active_workout")
		} else {
			add("general_chat")
		}
		return plan
	}
	if c.Session.Status != "in_progress" {
		return plan
	}
	if strings.Contains(t, "отмени тренировку") || t == "отмена" || strings.Contains(t, "не буду тренироваться") {
		add("abandon_workout")
		return plan
	}
	if strings.Contains(t, "добавь") {
		add("add_exercise")
		return plan
	}
	if strings.Contains(t, "?") || strings.Contains(t, "почему") || strings.Contains(t, "зачем") || strings.Contains(t, "как правильно") {
		if strings.Contains(t, "дальше") {
			add("get_current_workout_state")
		} else if isWorkoutQuestion(t, c) {
			add("fitness_question")
		} else {
			add("general_chat")
		}
		return plan
	}
	if t == "все" || t == "всё" || t == "готово" || t == "закончил" || t == "закончила" || t == "закончили" || strings.Contains(t, "закончил тренировку") || strings.Contains(t, "закончила тренировку") || strings.Contains(t, "тренировка окончена") {
		add("finish_workout")
		return plan
	}
	if strings.Contains(t, "замени") || strings.Contains(t, "занят") || strings.Contains(t, "не хочу") {
		add("replace_exercise")
		return plan
	}
	if strings.Contains(t, "пропусти") || strings.Contains(t, "убери") {
		add("skip_exercise")
		return plan
	}
	if strings.Contains(t, "минут") || strings.Contains(t, "без них") || strings.Contains(t, "ноги умерли") {
		add("replan_active_workout")
		return plan
	}
	if strings.Contains(t, "че дальше") || strings.Contains(t, "что дальше") || strings.Contains(t, "дальше?") {
		add("get_current_workout_state")
		return plan
	}
	if strings.Contains(t, "исправ") || strings.Contains(t, "не ") && strings.Contains(t, " а ") {
		add("correct_performance")
		return plan
	}
	if strings.Contains(t, "прогресс") || strings.Contains(t, "прошл") || strings.Contains(t, "истор") {
		if strings.Contains(t, "прогресс") {
			add("get_progression")
		} else {
			add("get_exercise_history")
		}
		return plan
	}
	if coachNumber.MatchString(t) && (coachNumber.FindString(t) == t || strings.Contains(t, "повтор") || strings.Contains(t, "подход") || strings.Contains(t, " раз") || strings.Contains(t, " кг") || strings.Contains(t, "кг") || strings.Contains(t, " на ") || strings.Contains(t, "сделал") || strings.Contains(t, "сделала") || strings.Contains(t, "легко") || strings.Contains(t, "тяжело")) {
		n := coachNumber.FindString(t)
		reps, _ := strconv.Atoi(n)
		if reps >= 1 && reps <= 200 {
			call := CoachToolCall{Name: "log_set", Reps: &reps}
			if strings.Contains(t, "перв") {
				x := 1
				call.SetNumber = &x
			}
			if strings.Contains(t, "втор") {
				x := 2
				call.SetNumber = &x
			}
			if strings.Contains(t, "треть") {
				x := 3
				call.SetNumber = &x
			}
			if m := coachWeight.FindStringSubmatch(t); len(m) > 1 {
				v, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
				call.WeightKg = &v
			}
			if strings.Contains(t, "легко") {
				call.Difficulty = "easy"
			} else if strings.Contains(t, "тяжело") {
				call.Difficulty = "hard"
			}
			plan.Calls = append(plan.Calls, call)
			return plan
		}
	}
	add("general_chat")
	return plan
}

func isWorkoutQuestion(text string, c *CoachContext) bool {
	for _, word := range []string{"подход", "повтор", "упражнен", "нагруз", "ассист", "вес", "тренировк"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return c != nil && c.Current != nil && strings.Contains(text, strings.ToLower(c.Current.ExerciseName))
}

func coachPromptAt(now time.Time, timezone string) string {
	loc := ResolveTimezone(timezone, "UTC")
	return strings.ReplaceAll(prompts.Default(prompts.KeyJasmineReply), "{{current_date}}", now.In(loc).Format("02.01.2006"))
}

func coachHistoryMessages(c *CoachContext) []openrouter.Message {
	if c == nil {
		return nil
	}
	history := make([]openrouter.Message, 0, len(c.RecentMessages))
	for _, message := range c.RecentMessages {
		role := "user"
		if message.Direction == telegram.DirectionOutbound {
			role = "assistant"
		}
		history = append(history, openrouter.Message{Role: role, Content: message.Text})
	}
	return history
}
