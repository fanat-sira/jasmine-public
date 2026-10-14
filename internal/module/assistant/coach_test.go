package assistant

import (
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"testing"
)

func TestCoachPlannerContextAndQuestion(t *testing.T) {
	reps := 12
	kg := 27.0
	c := &CoachContext{Session: &telegram.WorkoutSession{Status: "in_progress"}, Proposal: &WorkoutProposalDraft{Exercises: []ProposedExerciseDraft{{ExerciseID: "curl", ExerciseName: "Seated Leg Curl", Sets: []ProposedSetDraft{{Reps: &reps, WeightKg: &kg}, {Reps: &reps, WeightKg: &kg}}}}}, CurrentIndex: 0, CurrentSet: 2}
	c.Current = &c.Proposal.Exercises[0]
	cases := []struct{ text, want string }{{"15", "log_set"}, {"а почему тут 12 повторений", "fitness_question"}, {"че дальше", "get_current_workout_state"}, {"этот тренажер занят замени", "replace_exercise"}, {"времени осталось 15 минут", "replan_active_workout"}}
	for _, tc := range cases {
		plan := ruleCoachPlan(CoachTurn{Text: tc.text}, c)
		if len(plan.Calls) != 1 || plan.Calls[0].Name != tc.want {
			t.Fatalf("%s -> %+v", tc.text, plan)
		}
	}
}
func TestProgressionUsesStructuredMetrics(t *testing.T) {
	reps := 12
	normal := 23.0
	assist := 27.0
	base := 22.7
	added := 10.0
	mins := 15.0
	tests := []struct {
		candidate CandidateExercise
		set       workout.Set
		want      string
		value     float64
	}{
		{CandidateExercise{ExerciseID: "weighted", TrackingType: "weight_reps"}, workout.Set{Reps: &reps, WeightKg: &normal, Difficulty: "easy"}, "weight", 27},
		{CandidateExercise{ExerciseID: "assisted", TrackingType: "bodyweight_assisted"}, workout.Set{Reps: &reps, AssistanceKg: &assist, Difficulty: "hard"}, "assist", 27},
		{CandidateExercise{ExerciseID: "machine", TrackingType: "machine_weight"}, workout.Set{Reps: &reps, BaseWeightKg: &base, AddedWeightKg: &added, Difficulty: "easy"}, "added", 12.5},
		{CandidateExercise{ExerciseID: "cardio", TrackingType: "time_distance"}, workout.Set{TimeMin: &mins}, "time", 15},
	}
	for _, tc := range tests {
		history := []workout.Workout{{Exercises: []workout.WorkoutExercise{{ExerciseID: tc.candidate.ExerciseID, Sets: []workout.Set{tc.set}}}}}
		sig := ComputeProgression(tc.candidate, history)
		if sig == nil {
			t.Fatal("missing signal")
		}
		tc.candidate.Progression = sig
		proposed := ProposedSetDraft{SetNumber: 1, Reps: &reps, WeightKg: &normal, AssistanceKg: &assist, BaseWeightKg: &base, AddedWeightKg: &added}
		if tc.want == "time" {
			proposed = ProposedSetDraft{SetNumber: 1, TimeMin: ptr(10)}
		}
		p := &WorkoutProposalDraft{Exercises: []ProposedExerciseDraft{{ExerciseID: tc.candidate.ExerciseID, Sets: []ProposedSetDraft{proposed}}}}
		ApplyProgression(p, []CandidateExercise{tc.candidate})
		got := p.Exercises[0].Sets[0]
		switch tc.want {
		case "weight":
			if got.WeightKg == nil || *got.WeightKg != tc.value {
				t.Fatalf("weight %+v", got)
			}
		case "assist":
			if got.AssistanceKg == nil || *got.AssistanceKg < tc.value {
				t.Fatalf("assistance decreased after hard set: %+v", got)
			}
		case "added":
			if got.BaseWeightKg == nil || *got.BaseWeightKg != base || got.AddedWeightKg == nil || *got.AddedWeightKg != tc.value {
				t.Fatalf("machine components %+v", got)
			}
		case "time":
			if got.TimeMin == nil || *got.TimeMin != 15 {
				t.Fatalf("cardio %+v", got)
			}
		}
	}
}
func TestCoachShortSetParsesInheritedContext(t *testing.T) {
	c := &CoachContext{Session: &telegram.WorkoutSession{Status: "in_progress"}}
	p := ruleCoachPlan(CoachTurn{Text: "первый 12 на 27 легко"}, c)
	if len(p.Calls) != 1 || p.Calls[0].Reps == nil || *p.Calls[0].Reps != 12 || p.Calls[0].WeightKg == nil || *p.Calls[0].WeightKg != 27 || p.Calls[0].Difficulty != "easy" {
		t.Fatalf("bad set plan: %+v", p)
	}
}
func TestCoachExplicitReplyCurrentPriority(t *testing.T) {
	p := &WorkoutProposalDraft{Exercises: []ProposedExerciseDraft{{ExerciseID: "one", ExerciseName: "Seated Leg Curl"}, {ExerciseID: "two", ExerciseName: "Leg Press"}}}
	c := &CoachContext{Proposal: p, Current: &p.Exercises[0], CurrentIndex: 0, ReplyExerciseID: "two"}
	ex, _, amb := resolveCoachExercise(c, "", "15")
	if amb || ex.ExerciseID != "two" {
		t.Fatalf("reply target %+v %v", ex, amb)
	}
	ex, _, amb = resolveCoachExercise(c, "Seated Leg Curl", "15")
	if amb || ex.ExerciseID != "one" {
		t.Fatalf("explicit target %+v %v", ex, amb)
	}
}

func TestCoachPlannerMultipleIntents(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"ноги сегодня убитые давай минут на 40 без приседа", []string{"save_transient_state", "propose_workout"}},
		{"не люблю гравитон, дай тренировку", []string{"save_memory", "propose_workout"}},
	} {
		plan := ruleCoachPlan(CoachTurn{Text: tc.text}, &CoachContext{})
		if len(plan.Calls) != len(tc.want) {
			t.Fatalf("%q -> %+v", tc.text, plan)
		}
		for i, want := range tc.want {
			if plan.Calls[i].Name != want {
				t.Fatalf("%q: call %d = %s, want %s", tc.text, i, plan.Calls[i].Name, want)
			}
		}
	}
}

func TestCoachRoutesOpenFitnessQuestionsWithoutMutations(t *testing.T) {
	plan := ruleCoachPlan(CoachTurn{Text: "как правильно приседать?"}, &CoachContext{})
	if len(plan.Calls) != 1 || plan.Calls[0].Name != "general_chat" {
		t.Fatalf("fitness question: %+v", plan)
	}
	correction := ruleCoachPlan(CoachTurn{Text: "не 24 а 26"}, &CoachContext{})
	if len(correction.Calls) != 0 {
		t.Fatalf("historical correction diverted: %+v", correction)
	}
}

func TestCoachKeepsGeneralChatDuringWorkout(t *testing.T) {
	for _, status := range []string{"proposed", "in_progress"} {
		c := &CoachContext{Session: &telegram.WorkoutSession{Status: status}}
		for _, message := range []string{"какая столица Франции?", "поговорим про музыку", "мне 28"} {
			plan := ruleCoachPlan(CoachTurn{Text: message}, c)
			if len(plan.Calls) != 1 || plan.Calls[0].Name != "general_chat" {
				t.Errorf("%s, %q: %+v", status, message, plan)
			}
		}
	}
}
