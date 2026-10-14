package exercise

import "time"

type Exercise struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	NameEN       string    `json:"name_en,omitempty"`
	Type         string    `json:"type"`
	MuscleGroup  string    `json:"muscle_group"`
	Category     string    `json:"category,omitempty"`
	BodyRegion   string    `json:"body_region,omitempty"`
	TrackingType string    `json:"tracking_type,omitempty"`
	Difficulty   string    `json:"difficulty,omitempty"`
	Equipment    []string  `json:"equipment,omitempty"`
	IsCardio     bool      `json:"is_cardio"`
	IsDefault    bool      `json:"is_default"`
	CreatedAt    time.Time `json:"created_at"`
}
