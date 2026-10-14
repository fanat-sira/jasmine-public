package workout

import (
	"jasmine/internal/module/prompts"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/pkg/correlation"
)

var (
	ErrEmptyInput       = errors.New("workout command is empty")
	ErrInvalidLLMOutput = errors.New("invalid LLM workout output")
)

type Extractor struct {
	client  *openrouter.Client
	model   string
	prompts prompts.Store
}

func (e *Extractor) SetPromptStore(store prompts.Store) { e.prompts = store }

func NewExtractor(client *openrouter.Client, model string) *Extractor {
	return &Extractor{
		client: client,
		model:  model,
	}
}

func (e *Extractor) ExtractWorkoutDraft(ctx context.Context, input string) (*WorkoutDraft, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, ErrEmptyInput
	}

	if e.client == nil {
		return nil, fmt.Errorf("LLM client not configured (OPENROUTER_API_KEY is not set)")
	}

	systemPrompt, err := prompts.Resolve(ctx, e.prompts, prompts.KeyWorkoutExtraction)
	if err != nil {
		return nil, err
	}
	prompt := systemPrompt + trimmed

	aiCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	aiCtx = correlation.WithOperation(aiCtx, "workout_draft_extraction")

	start := time.Now()
	raw, err := e.client.SendChat(aiCtx, e.model, prompt)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("openrouter extraction call: %w", err)
	}

	payloadJSON := stripMarkdownFences(raw)
	if !json.Valid([]byte(payloadJSON)) {
		valErr := fmt.Errorf("%w: response is not valid JSON:\n%s", ErrInvalidLLMOutput, payloadJSON)
		e.client.RecordStructuredValidation(ctx, "workout_draft_extraction", e.model, latency, false, valErr)
		return nil, valErr
	}

	var draft WorkoutDraft
	if err := json.Unmarshal([]byte(payloadJSON), &draft); err != nil {
		valErr := fmt.Errorf("%w: failed to decode JSON: %w", ErrInvalidLLMOutput, err)
		e.client.RecordStructuredValidation(ctx, "workout_draft_extraction", e.model, latency, false, valErr)
		return nil, valErr
	}

	if len(draft.Exercises) == 0 {
		valErr := fmt.Errorf("%w: no exercises extracted from input", ErrInvalidLLMOutput)
		e.client.RecordStructuredValidation(ctx, "workout_draft_extraction", e.model, latency, false, valErr)
		return nil, valErr
	}

	// Validate & sanitize extracted sets
	for i := range draft.Exercises {
		ex := &draft.Exercises[i]
		ex.RawName = strings.TrimSpace(ex.RawName)
		if ex.RawName == "" {
			return nil, fmt.Errorf("%w: exercise at index %d has empty raw_name", ErrInvalidLLMOutput, i)
		}

		for j := range ex.Sets {
			s := &ex.Sets[j]
			if s.SetNumber <= 0 {
				s.SetNumber = j + 1
			}
			if s.Reps != nil && *s.Reps <= 0 {
				s.Reps = nil
			}
			if s.WeightKg != nil && *s.WeightKg < 0 {
				s.WeightKg = nil
			}
			if s.BaseWeightKg != nil && *s.BaseWeightKg < 0 {
				s.BaseWeightKg = nil
			}
			if s.AddedWeightKg != nil && *s.AddedWeightKg < 0 {
				s.AddedWeightKg = nil
			}
			if s.AssistanceKg != nil && *s.AssistanceKg < 0 {
				s.AssistanceKg = nil
			}
		}
	}

	if draft.WorkoutType == "" {
		draft.WorkoutType = "strength"
	}

	return &draft, nil
}

func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
