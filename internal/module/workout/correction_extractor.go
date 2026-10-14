package workout

import (
	"jasmine/internal/module/prompts"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
)

var ErrNotACorrection = errors.New("input is not a factual workout correction")

type CorrectionExtractor struct {
	client  *openrouter.Client
	model   string
	prompts prompts.Store
}

func (e *CorrectionExtractor) SetPromptStore(store prompts.Store) { e.prompts = store }

func NewCorrectionExtractor(client *openrouter.Client, model string) *CorrectionExtractor {
	return &CorrectionExtractor{
		client: client,
		model:  model,
	}
}

func (e *CorrectionExtractor) ExtractCorrectionDraft(ctx context.Context, input string) (*CorrectionDraft, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, ErrEmptyInput
	}

	// 1. Fast heuristic pre-check against non-correction patterns
	if IsHypotheticalOrQuestion(trimmed) {
		return &CorrectionDraft{IsCorrection: false}, nil
	}

	// 2. Pattern-based fast extraction for common concise patterns (e.g. "не 23, а 27", "27 а не 23")
	if fastDraft := tryFastCorrectionExtraction(trimmed); fastDraft != nil {
		return fastDraft, nil
	}

	if e.client == nil {
		return nil, fmt.Errorf("LLM client not configured for correction extraction")
	}

	systemPrompt, err := prompts.Resolve(ctx, e.prompts, prompts.KeyCorrectionExtraction)
	if err != nil {
		return nil, err
	}
	prompt := systemPrompt + trimmed
	aiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	raw, err := e.client.SendChat(aiCtx, e.model, prompt)
	if err != nil {
		return nil, fmt.Errorf("openrouter correction call: %w", err)
	}

	payloadJSON := stripMarkdownFences(raw)
	var draft CorrectionDraft
	if err := json.Unmarshal([]byte(payloadJSON), &draft); err != nil {
		return nil, fmt.Errorf("%w: malformed correction JSON: %w", ErrInvalidLLMOutput, err)
	}

	if !draft.IsCorrection || len(draft.Changes) == 0 {
		return &CorrectionDraft{IsCorrection: false}, nil
	}

	return &draft, nil
}

// IsHypotheticalOrQuestion catches non-factual statements.
func IsHypotheticalOrQuestion(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if strings.HasSuffix(t, "?") {
		return true
	}
	if strings.HasPrefix(t, "а если") || strings.HasPrefix(t, "если") ||
		strings.HasPrefix(t, "может") || strings.HasPrefix(t, "почему") ||
		strings.HasPrefix(t, "как думаешь") || strings.HasPrefix(t, "в следующий раз") {
		return true
	}
	return false
}

func tryFastCorrectionExtraction(text string) *CorrectionDraft {
	t := strings.ToLower(strings.TrimSpace(text))

	// Regex for "не X, а Y" or "не X а Y" (even with trailing feedback/clauses)
	re1 := regexp.MustCompile(`не\s+(\d+(?:\.\d+)?)(?:\s*кг)?\s*,?\s*а\s+(\d+(?:\.\d+)?)(?:\s*кг)?`)
	if m := re1.FindStringSubmatch(t); len(m) == 3 {
		var oldVal, newVal float64
		fmt.Sscanf(m[1], "%f", &oldVal)
		fmt.Sscanf(m[2], "%f", &newVal)
		return &CorrectionDraft{
			IsCorrection: true,
			Changes: []FieldChange{
				{
					Field:       "weight_kg",
					OldValue:    &oldVal,
					NewValue:    &newVal,
					SetSelector: "all",
				},
			},
			Confidence: 1.0,
		}
	}

	// Regex for "там было 2 подхода, не 3" or "2 подхода а не 3"
	reSets := regexp.MustCompile(`(?:там\s+было\s+)?(\d+)\s+(?:подхода|подходов|сета)\s*,?\s*(?:а\s+)?не\s+(\d+)`)
	if m := reSets.FindStringSubmatch(t); len(m) == 3 {
		var newVal, oldVal float64
		fmt.Sscanf(m[1], "%f", &newVal)
		fmt.Sscanf(m[2], "%f", &oldVal)
		return &CorrectionDraft{
			IsCorrection: true,
			Changes: []FieldChange{
				{
					Field:       "set_count",
					OldValue:    &oldVal,
					NewValue:    &newVal,
					SetSelector: "all",
				},
			},
			Confidence: 1.0,
		}
	}

	// Regex for "ассист 36, не 32" or "ассист был 36"
	reAssist := regexp.MustCompile(`ассист(?:\s+был)?\s+(\d+(?:\.\d+)?)(?:(?:\s*,\s*|\s+)не\s+(\d+(?:\.\d+)?))?`)
	if m := reAssist.FindStringSubmatch(t); len(m) >= 2 && m[1] != "" {
		var newVal float64
		fmt.Sscanf(m[1], "%f", &newVal)
		var oldVal *float64
		if len(m) == 3 && m[2] != "" {
			var ov float64
			fmt.Sscanf(m[2], "%f", &ov)
			oldVal = &ov
		}
		return &CorrectionDraft{
			IsCorrection: true,
			Changes: []FieldChange{
				{
					Field:       "assistance_kg",
					OldValue:    oldVal,
					NewValue:    &newVal,
					SetSelector: "all",
				},
			},
			Confidence: 1.0,
		}
	}

	// Regex for "+15 к тренажеру, не +10"
	reAdded := regexp.MustCompile(`\+(\d+(?:\.\d+)?)\s*(?:к\s+тренаж[её]ру)?\s*,?\s*не\s*\+?(\d+(?:\.\d+)?)`)
	if m := reAdded.FindStringSubmatch(t); len(m) == 3 {
		var newVal, oldVal float64
		fmt.Sscanf(m[1], "%f", &newVal)
		fmt.Sscanf(m[2], "%f", &oldVal)
		return &CorrectionDraft{
			IsCorrection: true,
			Changes: []FieldChange{
				{
					Field:       "added_weight_kg",
					OldValue:    &oldVal,
					NewValue:    &newVal,
					SetSelector: "all",
				},
			},
			Confidence: 1.0,
		}
	}

	// Regex for "во втором подходе 10 повторов"
	reSpecificSetReps := regexp.MustCompile(`во?\s+(первом|втором|третьем|четвертом|пятом|\d+)(?:-?[ое]м)?\s*(?:подходе)?\s+(\d+)\s*(?:повторов|повт|раз)`)
	if m := reSpecificSetReps.FindStringSubmatch(t); len(m) == 3 {
		setNum := parseSetOrdinal(m[1])
		var repsVal float64
		fmt.Sscanf(m[2], "%f", &repsVal)
		return &CorrectionDraft{
			IsCorrection: true,
			Changes: []FieldChange{
				{
					Field:       "reps",
					NewValue:    &repsVal,
					TargetSet:   &setNum,
					SetSelector: "specific",
				},
			},
			Confidence: 1.0,
		}
	}

	return nil
}

func parseSetOrdinal(s string) int {
	switch s {
	case "первом", "1":
		return 1
	case "втором", "2":
		return 2
	case "третьем", "3":
		return 3
	case "четвертом", "4":
		return 4
	case "пятом", "5":
		return 5
	default:
		var n int
		fmt.Sscanf(s, "%d", &n)
		if n <= 0 {
			return 1
		}
		return n
	}
}
