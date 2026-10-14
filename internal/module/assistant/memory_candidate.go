package assistant

import (
	"jasmine/internal/module/prompts"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/exercise"
	"jasmine/internal/pkg/correlation"
)

type ExerciseResolver interface {
	Resolve(ctx context.Context, rawName string) (*exercise.ResolutionResult, error)
}

type MemoryExtractor struct {
	client    *openrouter.Client
	model     string
	prompts   prompts.Store
	defaultTZ string
	resolver  ExerciseResolver
}

func (e *MemoryExtractor) SetPromptStore(store prompts.Store) { e.prompts = store }

func NewMemoryExtractor(client *openrouter.Client, model string) *MemoryExtractor {
	return &MemoryExtractor{
		client:    client,
		model:     model,
		defaultTZ: "UTC",
	}
}

func (e *MemoryExtractor) SetDefaultTimezone(tz string) {
	e.defaultTZ = tz
}

func (e *MemoryExtractor) SetExerciseResolver(resolver ExerciseResolver) {
	e.resolver = resolver
}

// ResolveTimezone implements the 3-tier hierarchy: user timezone -> configured default -> UTC.
func ResolveTimezone(userTZ string, defaultTZ string) *time.Location {
	if strings.TrimSpace(userTZ) != "" {
		if loc, err := time.LoadLocation(strings.TrimSpace(userTZ)); err == nil {
			return loc
		}
	}
	if strings.TrimSpace(defaultTZ) != "" {
		if loc, err := time.LoadLocation(strings.TrimSpace(defaultTZ)); err == nil {
			return loc
		}
	}
	return time.UTC
}

func (e *MemoryExtractor) ExtractCandidates(ctx context.Context, text string, userTZ *time.Location) ([]MemoryCandidate, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || IsHypotheticalOrQuestion(trimmed) {
		return nil, nil
	}

	if userTZ == nil {
		userTZ = ResolveTimezone("", e.defaultTZ)
	}

	// 1. High-confidence fast regex / pattern matching for common memory statements
	if fastCandidates := tryFastMemoryExtraction(trimmed, userTZ); len(fastCandidates) > 0 {
		return e.CanonicalizeCandidates(ctx, fastCandidates), nil
	}

	if e.client == nil {
		return nil, nil
	}

	systemPrompt, err := prompts.Resolve(ctx, e.prompts, prompts.KeyMemoryExtraction)
	if err != nil {
		return nil, err
	}
	prompt := systemPrompt + trimmed
	aiCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	aiCtx = correlation.WithOperation(aiCtx, "memory_extraction")

	start := time.Now()
	raw, err := e.client.SendChat(aiCtx, e.model, prompt)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("openrouter memory extraction: %w", err)
	}

	cleanJSON := stripMarkdownFences(raw)
	var candidates []MemoryCandidate
	if err := json.Unmarshal([]byte(cleanJSON), &candidates); err != nil {
		e.client.RecordStructuredValidation(ctx, "memory_extraction", e.model, latency, false, err)
		return nil, nil
	}
	e.client.RecordStructuredValidation(ctx, "memory_extraction", e.model, latency, true, nil)

	// Filter & compute valid_until timestamps
	var valid []MemoryCandidate
	for _, c := range candidates {
		if c.Confidence < 0.70 {
			continue // Drop low-confidence inferences conservatively
		}
		if c.Scope == "transient" || c.ValidUntilHint != "" {
			c.ValidUntil = ComputeValidUntilTimestamp(c.ValidUntilHint, c.Scope, userTZ)
		}
		if c.SourceText == "" {
			c.SourceText = trimmed
		}
		valid = append(valid, c)
	}

	return e.CanonicalizeCandidates(ctx, valid), nil
}

// CanonicalizeCandidates resolves raw targets to canonical backend-owned keys and deduplicates within the message batch.
func (e *MemoryExtractor) CanonicalizeCandidates(ctx context.Context, candidates []MemoryCandidate) []MemoryCandidate {
	if len(candidates) == 0 {
		return nil
	}

	seen := make(map[string]int)
	result := make([]MemoryCandidate, 0, len(candidates))

	for _, c := range candidates {
		target := strings.TrimSpace(c.TargetRaw)
		if target == "" && (c.Intent == "avoid_exercise" || strings.HasPrefix(c.Key, "avoid_") || c.Category == "preference") {
			target = extractAvoidanceTarget(c.SourceText)
			if target == "" && c.Value != "" {
				target = extractAvoidanceTarget(c.Value)
			}
		}

		if target != "" && (c.Intent == "avoid_exercise" || c.Category == "preference" || strings.HasPrefix(c.Key, "avoid_")) {
			c.Intent = "avoid_exercise"
			c.TargetRaw = target

			// Resolve target raw name through exercise catalog
			if e.resolver != nil {
				res, err := e.resolver.Resolve(ctx, target)
				if err == nil && res != nil && res.Exercise != nil &&
					(res.Status == exercise.ResolutionExact || res.Status == exercise.ResolutionFuzzy || res.Status == exercise.ResolutionLLMSelected) {
					c.Key = fmt.Sprintf("avoid_exercise:%s", res.Exercise.ID)
					c.Value = fmt.Sprintf("Исключить %s", res.Exercise.Name)
				} else {
					// Unresolved or ambiguous entity target: do not invent a slug key; skip persistence
					continue
				}
			} else {
				// No catalog resolver available: do not invent fake entity keys; skip persistence
				continue
			}
		} else if strings.HasPrefix(c.Key, "avoid_") && !strings.HasPrefix(c.Key, "avoid_exercise:") {
			rawName := strings.TrimPrefix(c.Key, "avoid_")
			if e.resolver != nil {
				res, err := e.resolver.Resolve(ctx, rawName)
				if err == nil && res != nil && res.Exercise != nil &&
					(res.Status == exercise.ResolutionExact || res.Status == exercise.ResolutionFuzzy || res.Status == exercise.ResolutionLLMSelected) {
					c.Key = fmt.Sprintf("avoid_exercise:%s", res.Exercise.ID)
					c.Value = fmt.Sprintf("Исключить %s", res.Exercise.Name)
				} else {
					continue
				}
			} else {
				continue
			}
		}

		// Semantic in-memory deduplication by Category + Key
		dedupKey := c.Category + ":" + c.Key
		if existingIdx, ok := seen[dedupKey]; ok {
			if c.Confidence >= result[existingIdx].Confidence {
				result[existingIdx] = c
			}
		} else {
			seen[dedupKey] = len(result)
			result = append(result, c)
		}
	}

	return result
}

// ComputeValidUntilTimestamp computes concrete expiration based on temporal keywords in user timezone.
func ComputeValidUntilTimestamp(hint, scope string, loc *time.Location) *time.Time {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)

	h := strings.ToLower(strings.TrimSpace(hint))
	if h == "today" || h == "сегодня" || (scope == "transient" && (h == "" || h == "today")) {
		// End of current local day in user timezone (23:59:59)
		endOfDay := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc)
		return &endOfDay
	}
	if h == "this_week" || h == "эту неделю" {
		// End of current local week (Sunday 23:59:59) in user timezone
		weekday := int(now.Weekday()) // Sunday = 0
		daysUntilSunday := (7 - weekday) % 7
		if daysUntilSunday == 0 {
			daysUntilSunday = 7
		}
		endOfWeek := time.Date(now.Year(), now.Month(), now.Day()+daysUntilSunday, 23, 59, 59, 0, loc)
		return &endOfWeek
	}
	if h == "now" || h == "сейчас" {
		t := now.Add(4 * time.Hour)
		return &t
	}
	return nil
}

func extractAvoidanceTarget(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	stripPatterns := []string{
		"больше никогда не давай",
		"больше не давай",
		"пока не давай",
		"пока что не давай",
		"временно не давай",
		"сегодня не давай",
		"сейчас не хочу",
		"пока не хочу",
		"не хочу сегодня",
		"не хочу",
		"не люблю",
		"пока убери",
		"убери",
		"исключить",
		"исключи",
		"без",
		"пока",
	}
	for _, p := range stripPatterns {
		if strings.Contains(t, p) {
			t = strings.Replace(t, p, "", 1)
		}
	}
	t = strings.Trim(t, " ,.!?\"'`")
	return strings.TrimSpace(t)
}

func extractReversalTarget(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	stripPatterns := []string{
		"снова можно",
		"опять можно",
		"можно давать",
		"забудь что я не люблю",
		"забудь что не люблю",
		"забудь про",
		"теперь норм",
		"уже не болит",
	}
	for _, p := range stripPatterns {
		if strings.Contains(t, p) {
			t = strings.Replace(t, p, "", 1)
		}
	}
	t = strings.Trim(t, " ,.!?\"'`")
	return strings.TrimSpace(t)
}

func tryFastMemoryExtraction(text string, loc *time.Location) []MemoryCandidate {
	t := strings.ToLower(strings.TrimSpace(text))

	// 1. Transient fatigue / temporary state ("сегодня ноги забиты", "сегодня устал")
	if strings.Contains(t, "ноги забиты") || strings.Contains(t, "забиты мышцы") || strings.Contains(t, "плохо спал") || (strings.Contains(t, "сегодня") && strings.Contains(t, "устал")) {
		exp := ComputeValidUntilTimestamp("today", "transient", loc)
		return []MemoryCandidate{
			{
				Category:       "transient_state",
				Key:            "fatigue_state",
				Value:          text,
				Scope:          "transient",
				Confidence:     0.95,
				ValidUntilHint: "today",
				ValidUntil:     exp,
				SourceText:     text,
			},
		}
	}

	// 2. Conditional avoidance with "пока", "пока что", "временно"
	if (strings.Contains(t, "пока") || strings.Contains(t, "временно") || strings.Contains(t, "сейчас не хочу")) && (strings.Contains(t, "не давай") || strings.Contains(t, "не хочу") || strings.Contains(t, "без")) {
		targetRaw := extractAvoidanceTarget(t)
		return []MemoryCandidate{
			{
				Category:   "preference",
				Intent:     "avoid_exercise",
				TargetRaw:  targetRaw,
				Key:        "conditional_avoidance",
				Value:      text,
				Scope:      "conditional",
				Confidence: 0.95,
				SourceText: text,
			},
		}
	}

	// 3. Transient avoidance for today ("сегодня не давай")
	if strings.Contains(t, "сегодня") && (strings.Contains(t, "не давай") || strings.Contains(t, "без")) {
		exp := ComputeValidUntilTimestamp("today", "transient", loc)
		targetRaw := extractAvoidanceTarget(t)
		return []MemoryCandidate{
			{
				Category:       "transient_state",
				Intent:         "avoid_exercise",
				TargetRaw:      targetRaw,
				Key:            "daily_avoidance",
				Value:          text,
				Scope:          "transient",
				Confidence:     0.95,
				ValidUntilHint: "today",
				ValidUntil:     exp,
				SourceText:     text,
			},
		}
	}

	// 4. Permanent dislike ("больше никогда", "не люблю болгарские выпады", "больше не давай")
	if strings.Contains(t, "не люблю") || strings.Contains(t, "больше никогда") || strings.Contains(t, "больше не давай") {
		targetRaw := extractAvoidanceTarget(t)
		return []MemoryCandidate{
			{
				Category:   "preference",
				Intent:     "avoid_exercise",
				TargetRaw:  targetRaw,
				Key:        "exercise_dislike",
				Value:      text,
				Scope:      "permanent",
				Confidence: 0.95,
				SourceText: text,
			},
		}
	}

	// 5. Reversals ("снова можно", "забудь что не люблю")
	if strings.Contains(t, "снова можно") || strings.Contains(t, "забудь что") || strings.Contains(t, "теперь норм") {
		targetRaw := extractReversalTarget(t)
		return []MemoryCandidate{
			{
				Category:   "preference",
				Intent:     "avoid_exercise",
				TargetRaw:  targetRaw,
				Key:        "exercise_dislike",
				Value:      text,
				Scope:      "permanent",
				Confidence: 0.95,
				IsReversal: true,
				SourceText: text,
			},
		}
	}

	// 6. Temporary environment ("эту неделю тренируюсь дома")
	if strings.Contains(t, "эту неделю") && strings.Contains(t, "дома") {
		exp := ComputeValidUntilTimestamp("this_week", "transient", loc)
		return []MemoryCandidate{
			{
				Category:       "equipment",
				Key:            "home_gym",
				Value:          "Тренировки дома (эту неделю)",
				Scope:          "transient",
				Confidence:     0.95,
				ValidUntilHint: "this_week",
				ValidUntil:     exp,
				SourceText:     text,
			},
		}
	}

	return nil
}

func IsHypotheticalOrQuestion(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if strings.HasSuffix(t, "?") {
		return true
	}
	if strings.HasPrefix(t, "может") || strings.HasPrefix(t, "а если") ||
		strings.HasPrefix(t, "если") || strings.HasPrefix(t, "почему") ||
		strings.HasPrefix(t, "как думаешь") || strings.HasPrefix(t, "наверное") {
		return true
	}
	return false
}

func stripMarkdownFences(raw string) string {
	s := strings.TrimSpace(raw)
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
