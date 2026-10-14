package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

func TestCoachSkillRegistersDomainActions(t *testing.T) {
	skill := NewCoachSkill(&CoachAgent{})
	required := map[string]bool{
		"coach.propose_workout": false, "coach.start_workout": false, "coach.log_set": false, "coach.log_exercise": false, "coach.log_machine_metrics": false, "coach.continue_workout_clarification": false, "coach.correct_performance": false, "coach.replace_exercise": false, "coach.skip_exercise": false, "coach.add_exercise": false, "coach.replan_active_workout": false, "coach.finish_workout": false, "coach.abandon_workout": false, "coach.get_current_workout_state": false, "coach.fitness_question": false,
	}
	for _, tool := range skill.Tools() {
		if _, ok := required[tool.Spec.Name]; ok {
			required[tool.Spec.Name] = true
		}
		if strings.HasPrefix(tool.Spec.Name, "coach.") && tool.Spec.RiskLevel == RiskLowWrite && tool.Spec.IdempotencyScope == "" {
			t.Errorf("write tool lacks idempotency scope: %s", tool.Spec.Name)
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("missing Coach tool %s", name)
		}
	}
}
func TestCoachFallbackRoutesContextualTurnsToTools(t *testing.T) {
	proposed := &CoachContext{Session: &telegram.WorkoutSession{Status: "proposed"}}
	active := &CoachContext{Session: &telegram.WorkoutSession{Status: "in_progress"}}
	cases := []struct {
		text string
		ctx  *CoachContext
		want string
		reps int
	}{
		{"че сегодня делаем", &CoachContext{}, "coach.propose_workout", 0},
		{"го", proposed, "coach.start_workout", 0},
		{"первый 12 на 27 легко", active, "coach.log_set", 12},
		{"15", active, "coach.log_set", 15},
		{"не 27 а 32", active, "coach.correct_performance", 0},
		{"этот тренажер занят замени", active, "coach.replace_exercise", 0},
		{"времени осталось 15 минут", active, "coach.replan_active_workout", 0},
		{"почему тут 12 а не 8", active, "coach.fitness_question", 0},
		{"че дальше", active, "coach.get_current_workout_state", 0},
		{"все", active, "coach.finish_workout", 0},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			step, err := fallbackCoachAction(AssistantEvent{Text: tc.text}, tc.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if step.Call == nil || step.Call.Name != tc.want {
				t.Fatalf("got %+v, want %s", step, tc.want)
			}
			if tc.reps > 0 {
				var args struct {
					Reps int `json:"reps"`
				}
				if err := json.Unmarshal(step.Call.Input, &args); err != nil {
					t.Fatal(err)
				}
				if args.Reps != tc.reps {
					t.Fatalf("reps=%d", args.Reps)
				}
			}
		})
	}
}

func TestSpecializedAdapterDoesNotAcceptWorkoutText(t *testing.T) {
	owner := &Orchestrator{}
	for _, message := range []string{"че сегодня делаем", "первый 12 на 27 легко", "15", "не 27 а 32", "все"} {
		allowed, err := isSpecializedText(context.Background(), owner, AssistantEvent{Text: message}, &CoachContext{})
		if err != nil || allowed {
			t.Fatalf("%q allowed=%v err=%v", message, allowed, err)
		}
	}
}

func TestCoachActionFactContainsConfirmedSet(t *testing.T) {
	reps := 15
	kg := 27.0
	c := &CoachContext{UserID: "u", Session: &telegram.WorkoutSession{ID: "session", Status: "in_progress"}, LastSet: &workout.Set{SetNumber: 2, Reps: &reps, WeightKg: &kg, Difficulty: "easy"}}
	response := &telegram.IngestionResponse{Success: true, ConfirmationHTML: "Записан подход", DomainReferences: []telegram.DomainReference{{Type: "workout", ID: "workout"}, {Type: "exercise", ID: "exercise", Name: "Seated Leg Curl"}}}
	got, err := coachTerminalResult(context.Background(), nil, AssistantEvent{UserID: "u"}, c, "log_set", response)
	if err != nil {
		t.Fatal(err)
	}
	var fact CoachActionFact
	if err := json.Unmarshal(got.Data, &fact); err != nil {
		t.Fatal(err)
	}
	if !fact.Applied || fact.WorkoutID != "workout" || fact.ExerciseID != "exercise" || fact.SetNumber != 2 || fact.Reps == nil || *fact.Reps != 15 || fact.WeightKg == nil || *fact.WeightKg != 27 {
		t.Fatalf("fact=%+v", fact)
	}
}
