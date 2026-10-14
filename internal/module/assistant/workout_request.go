package assistant

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

type WorkoutRequest struct {
	RequestedDurationMin *int     `json:"requested_duration_min,omitempty"`
	RequestedFocus       string   `json:"requested_focus,omitempty"`      // "upper", "lower", "full_body", "core", "arms"
	RequestedIntensity   string   `json:"requested_intensity,omitempty"`  // "easy", "normal", "hard"
	EnvironmentOverride  string   `json:"environment_override,omitempty"` // "home", "gym", "dumbbells_only"
	ExplicitExclusions   []string `json:"explicit_exclusions,omitempty"`
	ExplicitRequests     []string `json:"explicit_requests,omitempty"`
	RawText              string   `json:"raw_text"`
}

type WorkoutRequestExtractor struct{}

func NewWorkoutRequestExtractor() *WorkoutRequestExtractor {
	return &WorkoutRequestExtractor{}
}

// IsWorkoutGenerationRequest tests if the user message is a workout generation query.
func IsWorkoutGenerationRequest(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if strings.Contains(t, "что сегодня делать") || strings.Contains(t, "что сегодня?") ||
		strings.Contains(t, "дай тренировку") || strings.Contains(t, "составь тренировку") ||
		strings.Contains(t, "сделай тренировку") || strings.Contains(t, "напиши тренировку") ||
		strings.Contains(t, "план на сегодня") || strings.Contains(t, "что делать сегодня") ||
		strings.Contains(t, "сделай другую") || strings.Contains(t, "дай другую") ||
		strings.HasPrefix(t, "тренировка на") || strings.HasPrefix(t, "хочу сегодня") {
		return true
	}
	return false
}

// ExtractRequest parses duration, focus, environment, and intensity from natural text.
func (e *WorkoutRequestExtractor) ExtractRequest(ctx context.Context, text string) *WorkoutRequest {
	t := strings.ToLower(strings.TrimSpace(text))
	req := &WorkoutRequest{RawText: text}

	// 1. Duration extraction (e.g. "на 40 минут", "минут 40", "на час", "60 мин")
	if strings.Contains(t, "час") || strings.Contains(t, "60 мин") {
		dur := 60
		req.RequestedDurationMin = &dur
	} else {
		reDur := regexp.MustCompile(`(?:на\s*)?(\d{2,3})\s*(?:мин|минут|m)`)
		m := reDur.FindStringSubmatch(t)
		if len(m) != 2 {
			m = regexp.MustCompile(`минут\s*(?:на\s*)?(\d{2,3})`).FindStringSubmatch(t)
		}
		if len(m) == 2 {
			if v, err := strconv.Atoi(m[1]); err == nil && v >= 15 && v <= 180 {
				req.RequestedDurationMin = &v
			}
		}
	}

	// 2. Focus extraction
	if strings.Contains(t, "верх") || strings.Contains(t, "грудь") || strings.Contains(t, "спин") {
		req.RequestedFocus = "upper"
	} else if (strings.Contains(t, "ноги") && !strings.Contains(t, "убит") && !strings.Contains(t, "умер")) || strings.Contains(t, "низ") || strings.Contains(t, "ягодиц") {
		req.RequestedFocus = "lower"
	} else if strings.Contains(t, "фулбоди") || strings.Contains(t, "все тело") || strings.Contains(t, "фуллбоди") {
		req.RequestedFocus = "full_body"
	} else if strings.Contains(t, "пресс") || strings.Contains(t, "кор") {
		req.RequestedFocus = "core"
	}

	// 3. Intensity extraction
	if strings.Contains(t, "полегче") || strings.Contains(t, "легкую") || strings.Contains(t, "легкая") || strings.Contains(t, "восстановительную") {
		req.RequestedIntensity = "easy"
	} else if strings.Contains(t, "потяжелее") || strings.Contains(t, "интенсивную") || strings.Contains(t, "жесткую") {
		req.RequestedIntensity = "hard"
	}

	// 4. Environment / Equipment override
	if strings.Contains(t, "дома") || strings.Contains(t, "только гантели") || strings.Contains(t, "без тренажеров") {
		req.EnvironmentOverride = "home_dumbbells"
	}

	return req
}
