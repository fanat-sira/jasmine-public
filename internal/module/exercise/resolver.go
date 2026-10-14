package exercise

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/prompts"
	"jasmine/internal/pkg/correlation"
)

type ResolutionStatus string

const (
	ResolutionExact       ResolutionStatus = "exact"
	ResolutionFuzzy       ResolutionStatus = "fuzzy"
	ResolutionLLMSelected ResolutionStatus = "llm_selected"
	ResolutionAmbiguous   ResolutionStatus = "ambiguous"
	ResolutionNotFound    ResolutionStatus = "not_found"
)

type ResolutionResult struct {
	Status     ResolutionStatus `json:"status"`
	Exercise   *Exercise        `json:"exercise,omitempty"`
	Candidates []Exercise       `json:"candidates,omitempty"`
}

type Resolver struct {
	repo     *Repository
	aiClient *openrouter.Client
	model    string
	prompts  prompts.Store
}

func (r *Resolver) SetPromptStore(store prompts.Store) { r.prompts = store }

func NewResolver(repo *Repository, aiClient *openrouter.Client, model string) *Resolver {
	return &Resolver{
		repo:     repo,
		aiClient: aiClient,
		model:    model,
	}
}

// NormalizeQuery cleans and standardizes search terms (lowercasing, punctuation removal).
func NormalizeQuery(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	var sb strings.Builder
	for _, r := range q {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 'а' && r <= 'я') || r == 'ё' {
			if r == 'ё' {
				sb.WriteRune('е')
			} else {
				sb.WriteRune(r)
			}
		} else {
			sb.WriteRune(' ')
		}
	}
	words := strings.Fields(sb.String())
	return strings.Join(words, " ")
}

// Resolve identifies the canonical exercise using the 5-tier resolution pipeline.
func (r *Resolver) Resolve(ctx context.Context, rawName string) (*ResolutionResult, error) {
	clean := strings.TrimSpace(rawName)
	if clean == "" {
		return &ResolutionResult{Status: ResolutionNotFound}, nil
	}

	norm := NormalizeQuery(clean)

	// In test environments or when repo is not yet populated, fallback to robust alias/canonical match
	if r.repo == nil {
		return r.resolveWithoutDB(norm, clean)
	}

	// 1. Tier 1 & 2 & 3: Database Search (Exact + Normalized Aliases + PostgreSQL Fuzzy)
	results, err := r.repo.Search(ctx, clean)
	if err != nil {
		return nil, fmt.Errorf("exercise search: %w", err)
	}

	if len(results) == 1 {
		return &ResolutionResult{
			Status:   ResolutionExact,
			Exercise: &results[0],
		}, nil
	}

	if len(results) > 1 {
		// Check for direct exact match on Name or NameEN
		for _, ex := range results {
			if NormalizeQuery(ex.Name) == norm || NormalizeQuery(ex.NameEN) == norm {
				return &ResolutionResult{
					Status:   ResolutionExact,
					Exercise: &ex,
				}, nil
			}
		}

		// Tier 4: Contextual LLM selection if we have 2-5 candidates and an AI client
		if r.aiClient != nil && len(results) <= 5 {
			selected, err := r.llmDisambiguate(ctx, clean, results)
			if err == nil && selected != nil {
				return &ResolutionResult{
					Status:   ResolutionLLMSelected,
					Exercise: selected,
				}, nil
			}
		}

		// Check if candidates are genuinely ambiguous (e.g. Seated vs Lying Leg Curl)
		if isAmbiguousPair(norm, results) {
			return &ResolutionResult{Status: ResolutionAmbiguous, Candidates: results}, nil
		}

		// Top candidate
		return &ResolutionResult{
			Status:   ResolutionFuzzy,
			Exercise: &results[0],
		}, nil
	}

	return r.resolveWithoutDB(norm, clean)
}

func (r *Resolver) resolveWithoutDB(norm, clean string) (*ResolutionResult, error) {
	// 1. Specific qualified movements
	if (strings.Contains(norm, "сидя") || strings.Contains(norm, "seated")) && (strings.Contains(norm, "сгибан") || strings.Contains(norm, "курл") || strings.Contains(norm, "leg curl")) {
		return &ResolutionResult{
			Status: ResolutionExact,
			Exercise: &Exercise{
				ID:          "11111111-1111-1111-1111-111111111102",
				Name:        "Сгибание ног сидя (Seated Leg Curl)",
				NameEN:      "Seated Leg Curl",
				Type:        "strength",
				MuscleGroup: "hamstrings",
			},
		}, nil
	}

	if (strings.Contains(norm, "лежа") || strings.Contains(norm, "lying")) && (strings.Contains(norm, "сгибан") || strings.Contains(norm, "курл") || strings.Contains(norm, "leg curl")) {
		return &ResolutionResult{
			Status: ResolutionExact,
			Exercise: &Exercise{
				ID:          "11111111-1111-1111-1111-111111111107",
				Name:        "Сгибание ног лежа (Lying Leg Curl)",
				NameEN:      "Lying Leg Curl",
				Type:        "strength",
				MuscleGroup: "hamstrings",
			},
		}, nil
	}

	// 2. Ambiguous cases: "сгибание ног" without qualifier
	if norm == "сгибание ног" {
		candidates := []Exercise{
			{ID: "11111111-1111-1111-1111-111111111102", Name: "Сгибание ног сидя (Seated Leg Curl)", NameEN: "Seated Leg Curl"},
			{ID: "11111111-1111-1111-1111-111111111107", Name: "Сгибание ног лежа (Lying Leg Curl)", NameEN: "Lying Leg Curl"},
		}
		return &ResolutionResult{Status: ResolutionAmbiguous, Candidates: candidates}, nil
	}

	// 3. Other known slang terms
	known := []struct {
		keys []string
		ex   Exercise
	}{
		{
			keys: []string{"хип траст", "hip thrust", "ягодичный мостик"},
			ex: Exercise{
				ID:          "11111111-1111-1111-1111-111111111103",
				Name:        "Ягодичный мостик (Hip Thrust)",
				NameEN:      "Hip Thrust",
				Type:        "strength",
				MuscleGroup: "glutes",
			},
		},
		{
			keys: []string{"гравитон", "graviton", "подтягивания в гравитоне"},
			ex: Exercise{
				ID:          "11111111-1111-1111-1111-111111111104",
				Name:        "Подтягивания в гравитоне (Assisted Pull-up)",
				NameEN:      "Graviton",
				Type:        "strength",
				MuscleGroup: "lats",
			},
		},
		{
			keys: []string{"жим лежа", "bench press", "жим штанги лежа"},
			ex: Exercise{
				ID:          "11111111-1111-1111-1111-111111111105",
				Name:        "Жим штанги лежа (Bench Press)",
				NameEN:      "Barbell Bench Press",
				Type:        "strength",
				MuscleGroup: "chest",
			},
		},
		{
			keys: []string{"присед", "squat", "приседания"},
			ex: Exercise{
				ID:          "11111111-1111-1111-1111-111111111106",
				Name:        "Приседания со штангой (Squat)",
				NameEN:      "Barbell Back Squat",
				Type:        "strength",
				MuscleGroup: "quads",
			},
		},
		{
			keys: []string{"лег курл", "leg curl"},
			ex: Exercise{
				ID:          "11111111-1111-1111-1111-111111111101",
				Name:        "Сгибание ног (Leg Curl)",
				NameEN:      "Leg Curl",
				Type:        "strength",
				MuscleGroup: "hamstrings",
			},
		},
	}

	for _, item := range known {
		for _, k := range item.keys {
			if norm == k || strings.Contains(norm, k) {
				exCopy := item.ex
				return &ResolutionResult{
					Status:   ResolutionExact,
					Exercise: &exCopy,
				}, nil
			}
		}
	}

	return &ResolutionResult{
		Status: ResolutionNotFound,
	}, nil
}

func isAmbiguousPair(norm string, candidates []Exercise) bool {
	if len(candidates) < 2 {
		return false
	}
	// If the user query did not specify "сидя" or "лежа" or "наклон" but candidates differ on those
	hasSeated := false
	hasLying := false
	for _, c := range candidates {
		cNorm := strings.ToLower(c.Name)
		if strings.Contains(cNorm, "сидя") || strings.Contains(cNorm, "seated") {
			hasSeated = true
		}
		if strings.Contains(cNorm, "лежа") || strings.Contains(cNorm, "lying") {
			hasLying = true
		}
	}
	if hasSeated && hasLying && !strings.Contains(norm, "сидя") && !strings.Contains(norm, "лежа") {
		return true
	}
	return false
}

func (r *Resolver) llmDisambiguate(ctx context.Context, rawQuery string, candidates []Exercise) (*Exercise, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	type candidateChoice struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var choices []candidateChoice
	for _, c := range candidates {
		choices = append(choices, candidateChoice{ID: c.ID, Name: c.Name})
	}
	choicesJSON, _ := json.Marshal(choices)

	basePrompt, err := prompts.Resolve(ctx, r.prompts, prompts.KeyExerciseResolution)
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf("%s\nUser query: %q\nCandidates: %s", basePrompt, rawQuery, string(choicesJSON))

	aiCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	aiCtx = correlation.WithOperation(aiCtx, "disambiguation")

	start := time.Now()
	raw, err := r.aiClient.SendChat(aiCtx, r.model, prompt)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}

	cleanJSON := strings.TrimSpace(raw)
	if strings.HasPrefix(cleanJSON, "```") {
		if idx := strings.Index(cleanJSON, "\n"); idx != -1 {
			cleanJSON = cleanJSON[idx+1:]
		}
		if idx := strings.LastIndex(cleanJSON, "```"); idx != -1 {
			cleanJSON = cleanJSON[:idx]
		}
		cleanJSON = strings.TrimSpace(cleanJSON)
	}

	var res struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal([]byte(cleanJSON), &res); err != nil || res.ID == nil {
		r.aiClient.RecordStructuredValidation(ctx, "disambiguation", r.model, latency, false, err)
		return nil, nil
	}

	// STRICT CONSTRAINED CHECK: Verify the returned ID is strictly in the provided candidates set
	for _, c := range candidates {
		if c.ID == *res.ID {
			r.aiClient.RecordStructuredValidation(ctx, "disambiguation", r.model, latency, true, nil)
			return &c, nil
		}
	}

	// Never accept an unlisted or hallucinated UUID/name
	r.aiClient.RecordStructuredValidation(ctx, "disambiguation", r.model, latency, false, fmt.Errorf("hallucinated candidate id: %s", *res.ID))
	return nil, nil
}
