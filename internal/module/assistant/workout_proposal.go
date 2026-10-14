package assistant

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidProposalSchema = errors.New("invalid workout proposal schema")
	ErrUnknownExerciseID     = errors.New("proposal contains unknown exercise id not in candidate pool")
)

type WorkoutProposalDraft struct {
	Title                string                  `json:"title"`
	EstimatedDurationMin int                     `json:"estimated_duration_min"`
	Notes                string                  `json:"notes,omitempty"`
	AdaptationNote       string                  `json:"adaptation_note,omitempty"`
	Exercises            []ProposedExerciseDraft `json:"exercises"`
}

type ProposedExerciseDraft struct {
	ExerciseID   string             `json:"exercise_id"`
	ExerciseName string             `json:"exercise_name"`
	Sets         []ProposedSetDraft `json:"sets"`
	RestSec      int                `json:"rest_sec"`
	Notes        string             `json:"notes,omitempty"`
	ReasonCode   string             `json:"reason_code,omitempty"`
}

type ProposedSetDraft struct {
	SetNumber     int      `json:"set_number"`
	Reps          *int     `json:"reps,omitempty"`
	WeightKg      *float64 `json:"weight_kg,omitempty"`
	BaseWeightKg  *float64 `json:"base_weight_kg,omitempty"`
	AddedWeightKg *float64 `json:"added_weight_kg,omitempty"`
	AssistanceKg  *float64 `json:"assistance_kg,omitempty"`
	TimeMin       *int     `json:"time_min,omitempty"`
	DistanceKm    *float64 `json:"distance_km,omitempty"`
	Resistance    *string  `json:"resistance,omitempty"`
	RPETarget     *float64 `json:"rpe_target,omitempty"`
}

// ValidateProposal deterministically validates that all generated exercises strictly belong to the supplied candidate pool
// and that set parameters match the exercise tracking type and valid numeric ranges.
func ValidateProposal(draft *WorkoutProposalDraft, candidates []CandidateExercise) error {
	if draft == nil || len(draft.Exercises) == 0 {
		return fmt.Errorf("%w: proposal has no exercises", ErrInvalidProposalSchema)
	}

	candMap := make(map[string]CandidateExercise)
	for _, c := range candidates {
		candMap[c.ExerciseID] = c
	}

	seen := make(map[string]bool)
	for i, ex := range draft.Exercises {
		cand, ok := candMap[ex.ExerciseID]
		if !ok {
			return fmt.Errorf("%w: exercise_id %q is not in the candidate pool", ErrUnknownExerciseID, ex.ExerciseID)
		}
		if seen[ex.ExerciseID] {
			return fmt.Errorf("%w: duplicate exercise %s", ErrInvalidProposalSchema, ex.ExerciseID)
		}
		seen[ex.ExerciseID] = true
		if len(ex.Sets) == 0 {
			return fmt.Errorf("%w: exercise %q has 0 sets", ErrInvalidProposalSchema, ex.ExerciseName)
		}

		// Validate sets against tracking type
		for setIndex, s := range ex.Sets {
			if s.SetNumber != setIndex+1 {
				return fmt.Errorf("%w: non-sequential set number", ErrInvalidProposalSchema)
			}
			switch cand.TrackingType {
			case "weight_reps":
				if s.Reps == nil || *s.Reps <= 0 || *s.Reps > 200 {
					return fmt.Errorf("%w: exercise %q requires valid reps (1..200)", ErrInvalidProposalSchema, cand.Name)
				}
				if s.WeightKg != nil && (*s.WeightKg < 0 || *s.WeightKg > 1000) {
					return fmt.Errorf("%w: exercise %q invalid weight_kg", ErrInvalidProposalSchema, cand.Name)
				}
			case "machine_weight":
				if s.Reps == nil || *s.Reps < 1 || *s.Reps > 200 {
					return fmt.Errorf("%w: machine exercise requires valid reps", ErrInvalidProposalSchema)
				}
				if s.BaseWeightKg == nil && s.AddedWeightKg == nil {
					return fmt.Errorf("%w: machine components missing", ErrInvalidProposalSchema)
				}
				if s.WeightKg != nil || s.AssistanceKg != nil {
					return fmt.Errorf("%w: mixed machine load semantics", ErrInvalidProposalSchema)
				}
				for _, v := range []*float64{s.BaseWeightKg, s.AddedWeightKg} {
					if v != nil && (*v < 0 || *v > 1000) {
						return fmt.Errorf("%w: machine component out of range", ErrInvalidProposalSchema)
					}
				}
			case "bodyweight_reps":
				if s.Reps == nil || *s.Reps < 1 || *s.Reps > 200 {
					return fmt.Errorf("%w: bodyweight exercise requires reps", ErrInvalidProposalSchema)
				}
			case "bodyweight_assisted":
				if s.Reps == nil || *s.Reps <= 0 || *s.Reps > 200 {
					return fmt.Errorf("%w: assisted exercise %q requires valid reps (1..200)", ErrInvalidProposalSchema, cand.Name)
				}
				if s.AssistanceKg == nil || *s.AssistanceKg < 0 || *s.AssistanceKg > 300 {
					return fmt.Errorf("%w: assisted exercise %q requires valid assistance_kg (0..300)", ErrInvalidProposalSchema, cand.Name)
				}
			case "time_distance":
				if (s.TimeMin == nil || *s.TimeMin <= 0 || *s.TimeMin > 600) && (s.DistanceKm == nil || *s.DistanceKm <= 0 || *s.DistanceKm > 200) {
					return fmt.Errorf("%w: time/distance exercise %q requires valid time_min or distance_km", ErrInvalidProposalSchema, cand.Name)
				}
			}
		}

		// Populate canonical name if missing
		draft.Exercises[i].ExerciseName = cand.Name
	}

	if draft.EstimatedDurationMin <= 0 || draft.EstimatedDurationMin > 300 {
		draft.EstimatedDurationMin = 45
	}
	if draft.Title == "" {
		draft.Title = "Тренировка на сегодня"
	}

	return nil
}
