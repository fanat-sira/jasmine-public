package assistant

import (
	"jasmine/internal/module/prompts"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/pkg/correlation"
)

type WorkoutGenerator struct {
	aiClient *openrouter.Client
	model    string
	prompts  prompts.Store
}

func (g *WorkoutGenerator) SetPromptStore(store prompts.Store) { g.prompts = store }

func NewWorkoutGenerator(aiClient *openrouter.Client, model string) *WorkoutGenerator {
	return &WorkoutGenerator{
		aiClient: aiClient,
		model:    model,
	}
}

func (g *WorkoutGenerator) GenerateProposal(
	ctx context.Context,
	req *WorkoutRequest,
	convCtx *ConversationContext,
	candidates []CandidateExercise,
) (*WorkoutProposalDraft, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no candidate exercises available for generation")
	}

	// 1. If AI Client is available, call LLM
	if g.aiClient != nil {
		candJSON, err := json.Marshal(candidates)
		if err != nil {
			return nil, fmt.Errorf("marshal workout candidates: %w", err)
		}
		systemPrompt, err := prompts.Resolve(ctx, g.prompts, prompts.KeyWorkoutProposal)
		if err != nil {
			return nil, err
		}
		prompt := fmt.Sprintf("%s\n\nUser Context:\n%s\n\nCandidate Exercise Pool:\n%s\n\nGenerate today's workout:",
			systemPrompt,
			convCtx.FormattedPromptContext,
			string(candJSON),
		)

		aiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		aiCtx = correlation.WithOperation(aiCtx, "workout_generation")

		start := time.Now()
		raw, err := g.aiClient.SendChat(aiCtx, g.model, prompt)
		latency := time.Since(start).Milliseconds()
		if err == nil {
			cleanJSON := stripMarkdownFences(raw)
			var draft WorkoutProposalDraft
			if err := json.Unmarshal([]byte(cleanJSON), &draft); err != nil {
				g.aiClient.RecordStructuredValidation(ctx, "workout_generation", g.model, latency, false, err)
			} else if err := ValidateProposal(&draft, candidates); err != nil {
				g.aiClient.RecordStructuredValidation(ctx, "workout_generation", g.model, latency, false, err)
			} else {
				g.aiClient.RecordStructuredValidation(ctx, "workout_generation", g.model, latency, true, nil)
				ApplyProgression(&draft, candidates)
				if err := ValidateProposal(&draft, candidates); err != nil {
					return nil, err
				}
				return &draft, nil
			}
		}
	}

	// 2. Deterministic Fallback Generator
	draft, err := g.generateDeterministicFallback(req, convCtx, candidates)
	if err == nil {
		ApplyProgression(draft, candidates)
		err = ValidateProposal(draft, candidates)
	}
	return draft, err
}

func (g *WorkoutGenerator) generateDeterministicFallback(
	req *WorkoutRequest,
	convCtx *ConversationContext,
	candidates []CandidateExercise,
) (*WorkoutProposalDraft, error) {
	duration := 45
	if req != nil && req.RequestedDurationMin != nil {
		duration = *req.RequestedDurationMin
	}

	var proposedExs []ProposedExerciseDraft

	for _, cand := range candidates {
		if len(proposedExs) >= 4 {
			break
		}

		switch cand.Name {
		case "Hip Thrust":
			base := 22.7
			added := 10.0
			reps := 12
			proposedExs = append(proposedExs, ProposedExerciseDraft{
				ExerciseID:   cand.ExerciseID,
				ExerciseName: cand.Name,
				RestSec:      90,
				Sets: []ProposedSetDraft{
					{SetNumber: 1, Reps: &reps, BaseWeightKg: &base, AddedWeightKg: &added},
					{SetNumber: 2, Reps: &reps, BaseWeightKg: &base, AddedWeightKg: &added},
					{SetNumber: 3, Reps: &reps, BaseWeightKg: &base, AddedWeightKg: &added},
				},
			})
		case "Seated Leg Curl":
			wt := 23.0
			reps := 12
			proposedExs = append(proposedExs, ProposedExerciseDraft{
				ExerciseID:   cand.ExerciseID,
				ExerciseName: cand.Name,
				RestSec:      75,
				Sets: []ProposedSetDraft{
					{SetNumber: 1, Reps: &reps, WeightKg: &wt},
					{SetNumber: 2, Reps: &reps, WeightKg: &wt},
					{SetNumber: 3, Reps: &reps, WeightKg: &wt},
				},
			})
		case "Graviton Pull-Ups":
			assist := 32.0
			reps := 10
			proposedExs = append(proposedExs, ProposedExerciseDraft{
				ExerciseID:   cand.ExerciseID,
				ExerciseName: cand.Name,
				RestSec:      90,
				Sets: []ProposedSetDraft{
					{SetNumber: 1, Reps: &reps, AssistanceKg: &assist},
					{SetNumber: 2, Reps: &reps, AssistanceKg: &assist},
				},
			})
		case "Dead Bug":
			reps := 10
			proposedExs = append(proposedExs, ProposedExerciseDraft{
				ExerciseID:   cand.ExerciseID,
				ExerciseName: cand.Name,
				RestSec:      60,
				Sets: []ProposedSetDraft{
					{SetNumber: 1, Reps: &reps},
					{SetNumber: 2, Reps: &reps},
					{SetNumber: 3, Reps: &reps},
				},
			})
		case "Stationary Bike":
			min := 15
			proposedExs = append(proposedExs, ProposedExerciseDraft{
				ExerciseID:   cand.ExerciseID,
				ExerciseName: cand.Name,
				Sets: []ProposedSetDraft{
					{SetNumber: 1, TimeMin: &min},
				},
			})
		default:
			if len(proposedExs) < 3 {
				reps := 10
				wt := 15.0
				proposedExs = append(proposedExs, ProposedExerciseDraft{
					ExerciseID:   cand.ExerciseID,
					ExerciseName: cand.Name,
					RestSec:      60,
					Sets: []ProposedSetDraft{
						{SetNumber: 1, Reps: &reps, WeightKg: &wt},
						{SetNumber: 2, Reps: &reps, WeightKg: &wt},
						{SetNumber: 3, Reps: &reps, WeightKg: &wt},
					},
				})
			}
		}
	}

	draft := &WorkoutProposalDraft{
		Title:                "Тренировка на сегодня",
		EstimatedDurationMin: duration,
		AdaptationNote:       "",
		Exercises:            proposedExs,
	}

	if err := ValidateProposal(draft, candidates); err != nil {
		return nil, fmt.Errorf("validate fallback proposal: %w", err)
	}

	return draft, nil
}
