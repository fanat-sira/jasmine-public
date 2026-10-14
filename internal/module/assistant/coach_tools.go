package assistant

import (
	"fmt"
	"strings"
)

// CoachToolRegistry is the application boundary for planner proposals. Tool
// names and preconditions are controlled here; handlers use domain services.
type CoachToolSpec struct {
	Name            string
	Mutates         bool
	RequiresSession bool
	AllowedStatus   string
}
type CoachToolRegistry struct{ specs map[string]CoachToolSpec }

func NewCoachToolRegistry() *CoachToolRegistry {
	specs := []CoachToolSpec{
		{Name: "get_active_session"}, {Name: "get_current_workout_state", RequiresSession: true}, {Name: "get_exercise_history"}, {Name: "get_progression"},
		{Name: "propose_workout", Mutates: true}, {Name: "start_workout", Mutates: true, RequiresSession: true, AllowedStatus: "proposed"},
		{Name: "log_set", Mutates: true, RequiresSession: true, AllowedStatus: "in_progress"}, {Name: "correct_performance", Mutates: true, RequiresSession: true, AllowedStatus: "in_progress"},
		{Name: "add_exercise", Mutates: true, RequiresSession: true}, {Name: "abandon_workout", Mutates: true, RequiresSession: true}, {Name: "replace_exercise", Mutates: true, RequiresSession: true}, {Name: "skip_exercise", Mutates: true, RequiresSession: true}, {Name: "replan_active_workout", Mutates: true, RequiresSession: true},
		{Name: "finish_workout", Mutates: true, RequiresSession: true, AllowedStatus: "in_progress"}, {Name: "save_transient_state", Mutates: true}, {Name: "save_memory", Mutates: true}, {Name: "fitness_question"}, {Name: "general_chat"},
	}
	r := &CoachToolRegistry{specs: make(map[string]CoachToolSpec, len(specs))}
	for _, spec := range specs {
		r.specs[spec.Name] = spec
	}
	return r
}
func (r *CoachToolRegistry) Validate(call CoachToolCall, c *CoachContext) error {
	spec, ok := r.specs[call.Name]
	if !ok {
		return fmt.Errorf("unknown coach tool %q", call.Name)
	}
	if call.Name == "propose_workout" && c.Session != nil && c.Session.Status == "in_progress" {
		return fmt.Errorf("сначала заверши текущую тренировку")
	}
	if spec.RequiresSession && c.Session == nil {
		return fmt.Errorf("%s requires an active session", call.Name)
	}
	if spec.AllowedStatus != "" && c.Session != nil && c.Session.Status != spec.AllowedStatus {
		return fmt.Errorf("%s requires %s session", call.Name, spec.AllowedStatus)
	}
	if call.Name == "log_set" {
		t := strings.ToLower(c.TurnText)
		if strings.Contains(t, "почему") || strings.Contains(t, "?") || strings.Contains(t, "времени осталось") || strings.Contains(t, "че дальше") || strings.Contains(t, "что дальше") {
			return fmt.Errorf("вопрос не является отчётом о подходе")
		}
		if call.Reps == nil {
			return fmt.Errorf("не указаны выполненные повторы")
		}
		found := false
		for _, n := range coachNumber.FindAllString(t, -1) {
			if n == fmt.Sprint(*call.Reps) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("повторы не совпадают с сообщением")
		}
	}
	if call.SetNumber != nil && (*call.SetNumber < 1 || *call.SetNumber > 100) {
		return fmt.Errorf("set number out of range")
	}
	if call.Reps != nil && (*call.Reps < 1 || *call.Reps > 200) {
		return fmt.Errorf("repetitions out of range")
	}
	if call.WeightKg != nil && (*call.WeightKg < 0 || *call.WeightKg > 1000) {
		return fmt.Errorf("weight out of range")
	}
	if call.AssistanceKg != nil && (*call.AssistanceKg < 0 || *call.AssistanceKg > 300) {
		return fmt.Errorf("assistance out of range")
	}
	if call.DurationMin != nil && (*call.DurationMin < 5 || *call.DurationMin > 300) {
		return fmt.Errorf("duration out of range")
	}
	return nil
}
