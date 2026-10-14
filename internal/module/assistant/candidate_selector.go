package assistant

import (
	"context"
	"fmt"
	"strings"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

type CandidateExercise struct {
	ExerciseID        string             `json:"exercise_id"`
	Name              string             `json:"name"`
	TrackingType      string             `json:"tracking_type"` // "weight_reps", "machine_weight", "bodyweight_assisted", "time_distance", "bodyweight_reps"
	Category          string             `json:"category"`      // "lower", "upper", "core", "cardio"
	Equipment         string             `json:"equipment"`     // "dumbbell", "barbell", "machine", "graviton", "bike", "bodyweight"
	RecentPerformance string             `json:"recent_performance,omitempty"`
	RecentFeedback    string             `json:"recent_feedback,omitempty"`
	Progression       *ProgressionSignal `json:"progression,omitempty"`
}

type CandidateSelector struct {
	exerciseRepo *exercise.Repository
}

func NewCandidateSelector(exerciseRepo *exercise.Repository) *CandidateSelector {
	return &CandidateSelector{exerciseRepo: exerciseRepo}
}

// SelectCandidates builds a filtered candidate pool of canonical exercises with real UUIDs.
func (s *CandidateSelector) SelectCandidates(
	ctx context.Context,
	req *WorkoutRequest,
	activeMemories []telegram.UserMemory,
	recentWorkouts []workout.Workout,
	feedbackEvents []telegram.UserFeedbackEvent,
) ([]CandidateExercise, error) {
	var allExercises []CandidateExercise
	if s.exerciseRepo != nil {
		dbList, err := s.exerciseRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list candidate exercises: %w", err)
		}
		if len(dbList) > 0 {
			allExercises = make([]CandidateExercise, 0, len(dbList))
			for _, e := range dbList {
				eq := ""
				if len(e.Equipment) > 0 {
					eq = e.Equipment[0]
				}
				allExercises = append(allExercises, CandidateExercise{
					ExerciseID:   e.ID,
					Name:         e.Name,
					TrackingType: e.TrackingType,
					Category:     e.Category,
					Equipment:    eq,
				})
			}
		}
	}
	if len(allExercises) == 0 && s.exerciseRepo == nil {
		allExercises = s.getDefaultCatalog()
	}

	// 1. Build exclusion filter list from active persistent & conditional memories
	exclusions := make(map[string]bool)
	isHomeOnly := false

	for _, m := range activeMemories {
		if m.SupersededBy != nil {
			continue
		}
		keyLower := strings.ToLower(m.Key)
		valLower := strings.ToLower(string(m.Value))

		// Check dislikes / conditional avoidance
		if (m.Category == "preference" || m.Category == "transient_state") && (strings.Contains(valLower, "не давай") || strings.Contains(valLower, "не люблю") || strings.Contains(valLower, "без") || strings.Contains(valLower, "исключить") || strings.HasPrefix(keyLower, "avoid_exercise:")) {
			if strings.HasPrefix(keyLower, "avoid_exercise:") {
				target := strings.TrimPrefix(keyLower, "avoid_exercise:")
				exclusions[target] = true
			}
			if strings.Contains(valLower, "болгарск") || strings.Contains(keyLower, "lunge") {
				exclusions["bulgarian_split_squat"] = true
				exclusions["lunges"] = true
			}
			if strings.Contains(valLower, "гравитон") || strings.Contains(keyLower, "graviton") {
				exclusions["graviton"] = true
			}
			if strings.Contains(valLower, "дорожк") || strings.Contains(keyLower, "treadmill") {
				exclusions["treadmill"] = true
			}
			if strings.Contains(valLower, "лег пресс") || strings.Contains(valLower, "жим ног") {
				exclusions["leg_press"] = true
			}
		}

		if m.Category == "equipment" && (strings.Contains(valLower, "дома") || strings.Contains(valLower, "только гантели")) {
			isHomeOnly = true
		}
	}

	if req != nil && req.EnvironmentOverride == "home_dumbbells" {
		isHomeOnly = true
	}

	// 2. Filter exercises
	var candidates []CandidateExercise
	for _, ex := range allExercises {
		if exclusions[ex.Equipment] || exclusions[strings.ToLower(ex.Name)] || exclusions[ex.ExerciseID] {
			continue // Excluded by user constraint
		}

		if req != nil && strings.Contains(strings.ToLower(req.RawText), "без присед") && (strings.Contains(strings.ToLower(ex.Name), "squat") || strings.Contains(strings.ToLower(ex.Name), "присед")) {
			continue
		}
		if req != nil && strings.Contains(strings.ToLower(req.RawText), "ноги") && strings.Contains(strings.ToLower(req.RawText), "убит") && ex.Category == "lower" {
			continue
		}

		if req != nil && (req.RequestedFocus == "upper" || req.RequestedFocus == "lower" || req.RequestedFocus == "core") && ex.Category != "" && ex.Category != req.RequestedFocus {
			continue
		}
		if isHomeOnly && ex.Equipment != "dumbbell" && ex.Equipment != "bodyweight" {
			continue // Exclude gym machines when user is training at home
		}

		// Attach recent performance & feedback if present
		ex.RecentPerformance = s.findRecentPerformance(ex.Name, ex.ExerciseID, recentWorkouts)
		ex.RecentFeedback = s.findRecentFeedback(ex.ExerciseID, feedbackEvents)
		ex.Progression = ComputeProgression(ex, recentWorkouts)

		candidates = append(candidates, ex)
	}

	return candidates, nil
}

func (s *CandidateSelector) findRecentPerformance(name, exID string, workouts []workout.Workout) string {
	for _, w := range workouts {
		for _, we := range w.Exercises {
			if we.ExerciseID == exID || strings.EqualFold(we.ExerciseName, name) {
				if len(we.Sets) > 0 {
					var parts []string
					for _, set := range we.Sets {
						reps := 0
						if set.Reps != nil {
							reps = *set.Reps
						}
						if set.WeightKg != nil {
							parts = append(parts, fmt.Sprintf("%d×%d @ %.1f kg", len(we.Sets), reps, *set.WeightKg))
							break
						} else if set.AssistanceKg != nil {
							parts = append(parts, fmt.Sprintf("%d×%d @ assist %.1f kg", len(we.Sets), reps, *set.AssistanceKg))
							break
						} else if set.BaseWeightKg != nil || set.AddedWeightKg != nil {
							b := 0.0
							a := 0.0
							if set.BaseWeightKg != nil {
								b = *set.BaseWeightKg
							}
							if set.AddedWeightKg != nil {
								a = *set.AddedWeightKg
							}
							parts = append(parts, fmt.Sprintf("%d×%d @ machine %.1f + %.1f kg", len(we.Sets), reps, b, a))
							break
						}
					}
					return strings.Join(parts, ", ")
				}
			}
		}
	}
	return ""
}

func (s *CandidateSelector) findRecentFeedback(exID string, feedbacks []telegram.UserFeedbackEvent) string {
	for _, fb := range feedbacks {
		if fb.ExerciseID != nil && *fb.ExerciseID == exID {
			return fb.RawText
		}
	}
	return ""
}

func (s *CandidateSelector) getDefaultCatalog() []CandidateExercise {
	return []CandidateExercise{
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000001",
			Name:         "Hip Thrust",
			TrackingType: "machine_weight",
			Category:     "lower",
			Equipment:    "machine",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000002",
			Name:         "Seated Leg Curl",
			TrackingType: "weight_reps",
			Category:     "lower",
			Equipment:    "machine",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000003",
			Name:         "Graviton Pull-Ups",
			TrackingType: "bodyweight_assisted",
			Category:     "upper",
			Equipment:    "graviton",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000004",
			Name:         "Dumbbell Chest Press",
			TrackingType: "weight_reps",
			Category:     "upper",
			Equipment:    "dumbbell",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000005",
			Name:         "Dumbbell Bulgarian Split Squat",
			TrackingType: "weight_reps",
			Category:     "lower",
			Equipment:    "dumbbell",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000006",
			Name:         "Dead Bug",
			TrackingType: "bodyweight_reps",
			Category:     "core",
			Equipment:    "bodyweight",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000007",
			Name:         "Stationary Bike",
			TrackingType: "time_distance",
			Category:     "cardio",
			Equipment:    "bike",
		},
		{
			ExerciseID:   "00000000-0000-0000-0000-000000000008",
			Name:         "Leg Press",
			TrackingType: "weight_reps",
			Category:     "lower",
			Equipment:    "machine",
		},
	}
}
