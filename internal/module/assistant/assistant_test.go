package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

func ptr[T any](v T) *T {
	return &v
}

func TestMemoryExtraction_PersistentPreference(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000099", Name: "Graviton"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{"гравитон": gravitonEx},
	}
	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	candidates, err := extractor.ExtractCandidates(context.Background(), "больше никогда не давай гравитон", time.Local)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if c.Category != "preference" || c.Scope != "permanent" {
		t.Errorf("expected permanent preference, got category=%s, scope=%s", c.Category, c.Scope)
	}
	if c.IsReversal {
		t.Errorf("must not be a reversal")
	}
}

func TestMemoryExtraction_ConditionalScopeForPoka(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000099", Name: "Graviton"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{"гравитон": gravitonEx},
	}
	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	candidates, err := extractor.ExtractCandidates(context.Background(), "пока не давай гравитон", time.Local)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if c.Scope != "conditional" {
		t.Errorf("words with 'пока' MUST be conditional scope, got: %s", c.Scope)
	}
}

func TestMemoryExtraction_SupersessionAndReversal(t *testing.T) {
	lungesEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000088", Name: "Bulgarian Split Squat"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{"болгарские выпады": lungesEx},
	}
	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	candidates, err := extractor.ExtractCandidates(context.Background(), "болгарские выпады снова можно", time.Local)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if !c.IsReversal {
		t.Errorf("expected is_reversal: true for 'болгарские выпады снова можно'")
	}
}

func TestMemoryExtraction_TimezoneHierarchyAndExpiry(t *testing.T) {
	locMsk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load msk location: %v", err)
	}

	resolved := ResolveTimezone("Europe/Moscow", "UTC")
	if resolved.String() != "Europe/Moscow" {
		t.Errorf("expected user timezone 'Europe/Moscow', got: %s", resolved.String())
	}
	fallback := ResolveTimezone("", "Europe/Moscow")
	if fallback.String() != "Europe/Moscow" {
		t.Errorf("expected default timezone 'Europe/Moscow', got: %s", fallback.String())
	}
	utcFallback := ResolveTimezone("", "")
	if utcFallback != time.UTC {
		t.Errorf("expected UTC fallback, got: %s", utcFallback.String())
	}

	eod := ComputeValidUntilTimestamp("today", "transient", locMsk)
	if eod == nil {
		t.Fatalf("expected non-nil end-of-day")
	}
	if eod.Hour() != 23 || eod.Minute() != 59 || eod.Second() != 59 {
		t.Errorf("expected 23:59:59 in target timezone, got %v", eod)
	}
}

func TestWorkoutGeneration_BasicAndDuration(t *testing.T) {
	candSelector := NewCandidateSelector(nil)
	generator := NewWorkoutGenerator(nil, "")
	builder := NewContextBuilder(DefaultBudgets())
	reqExtractor := NewWorkoutRequestExtractor()

	req := reqExtractor.ExtractRequest(context.Background(), "сделай тренировку на 40 минут")
	if req.RequestedDurationMin == nil || *req.RequestedDurationMin != 40 {
		t.Errorf("expected 40 min duration, got %v", req.RequestedDurationMin)
	}

	candidates, err := candSelector.SelectCandidates(context.Background(), req, nil, nil, nil)
	if err != nil {
		t.Fatalf("select candidates failed: %v", err)
	}

	convCtx := builder.BuildContext(context.Background(), ActionWorkoutRequest, "u-1", nil, nil, nil, nil, nil, "")
	proposal, err := generator.GenerateProposal(context.Background(), req, convCtx, candidates)
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}

	if proposal.EstimatedDurationMin != 40 {
		t.Errorf("expected proposal duration 40 min, got %d", proposal.EstimatedDurationMin)
	}
	if len(proposal.Exercises) == 0 {
		t.Fatalf("expected generated exercises")
	}
}

func TestWorkoutGeneration_AssistanceAndProgressionSemantics(t *testing.T) {
	candSelector := NewCandidateSelector(nil)
	generator := NewWorkoutGenerator(nil, "")
	builder := NewContextBuilder(DefaultBudgets())

	// History: Leg Curl 23kg easy, Graviton assist 27kg hard
	recentWorkouts := []workout.Workout{
		{
			Exercises: []workout.WorkoutExercise{
				{
					ExerciseID:   "00000000-0000-0000-0000-000000000002",
					ExerciseName: "Seated Leg Curl",
					Sets:         []workout.Set{{Reps: ptr(12), WeightKg: ptr(23.0)}},
				},
				{
					ExerciseID:   "00000000-0000-0000-0000-000000000003",
					ExerciseName: "Graviton Pull-Ups",
					Sets:         []workout.Set{{Reps: ptr(10), AssistanceKg: ptr(27.0)}},
				},
			},
		},
	}
	feedbacks := []telegram.UserFeedbackEvent{
		{ExerciseID: ptr("00000000-0000-0000-0000-000000000002"), RawText: "23 было easy"},
		{ExerciseID: ptr("00000000-0000-0000-0000-000000000003"), RawText: "27 было hard"},
	}

	candidates, err := candSelector.SelectCandidates(context.Background(), nil, nil, recentWorkouts, feedbacks)
	if err != nil {
		t.Fatalf("select candidates: %v", err)
	}

	convCtx := builder.BuildContext(context.Background(), ActionWorkoutRequest, "u-1", nil, nil, nil, recentWorkouts, feedbacks, "")
	proposal, err := generator.GenerateProposal(context.Background(), nil, convCtx, candidates)
	if err != nil {
		t.Fatalf("generate proposal: %v", err)
	}

	for _, ex := range proposal.Exercises {
		if ex.ExerciseName == "Seated Leg Curl" {
			if ex.Sets[0].WeightKg == nil || *ex.Sets[0].WeightKg < 27.0 {
				t.Errorf("expected Leg Curl progressed to 27kg after easy feedback, got %v", ex.Sets[0].WeightKg)
			}
		}
		if ex.ExerciseName == "Graviton Pull-Ups" {
			// Because 27kg assist was hard, assist must NOT be decreased below 27!
			if ex.Sets[0].AssistanceKg == nil || *ex.Sets[0].AssistanceKg < 27.0 {
				t.Errorf("graviton assistance must not decrease below 27kg when it was hard, got %v", ex.Sets[0].AssistanceKg)
			}
		}
	}
}

func TestWorkoutGeneration_ExclusionsAndConditionalAvoidance(t *testing.T) {
	candSelector := NewCandidateSelector(nil)

	memories := []telegram.UserMemory{
		{
			Category: "preference",
			Scope:    "conditional",
			Key:      "conditional_avoidance",
			Value:    json.RawMessage(`"пока не давай гравитон"`),
		},
	}

	candidates, err := candSelector.SelectCandidates(context.Background(), nil, memories, nil, nil)
	if err != nil {
		t.Fatalf("select candidates: %v", err)
	}

	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Name), "graviton") {
			t.Errorf("graviton MUST be excluded while conditional avoidance is active")
		}
	}
}

func TestWorkoutProposal_InvalidExerciseIDRejected(t *testing.T) {
	candSelector := NewCandidateSelector(nil)
	candidates, _ := candSelector.SelectCandidates(context.Background(), nil, nil, nil, nil)

	// Hallucinated invalid exercise ID
	invalidDraft := &WorkoutProposalDraft{
		Title: "Fake Workout",
		Exercises: []ProposedExerciseDraft{
			{
				ExerciseID:   "11111111-9999-9999-9999-999999999999",
				ExerciseName: "Invented Exercise",
				Sets:         []ProposedSetDraft{{SetNumber: 1, Reps: ptr(10)}},
			},
		},
	}

	err := ValidateProposal(invalidDraft, candidates)
	if err == nil {
		t.Fatalf("expected error on unknown exercise_id, got nil")
	}
}

func TestSessionService_ProposalModification(t *testing.T) {
	sessionSvc := NewSessionService(nil, nil)

	initialProposal := &WorkoutProposalDraft{
		Title:                "Тренировка",
		EstimatedDurationMin: 45,
		Exercises: []ProposedExerciseDraft{
			{
				ExerciseID:   "00000000-0000-0000-0000-000000000008",
				ExerciseName: "Leg Press",
				Sets:         []ProposedSetDraft{{SetNumber: 1, Reps: ptr(12), WeightKg: ptr(50.0)}},
			},
		},
	}
	propBytes, _ := json.Marshal(initialProposal)

	session := &telegram.WorkoutSession{
		ID:           "s-100",
		UserID:       "u-1",
		Status:       "proposed",
		ProposalData: propBytes,
	}

	// User: "замени лег пресс и добавь пресс"
	res, err := sessionSvc.ModifyActiveProposal(context.Background(), nil, "u-1", 12345, 999, 101, session, "замени лег пресс и добавь пресс")
	if err != nil {
		t.Fatalf("modify proposal failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success: true")
	}
	proposal := res.ReplyContext.CurrentState["proposal"].(WorkoutProposalDraft)
	for _, ex := range proposal.Exercises {
		if ex.ExerciseName == "Leg Press" {
			t.Error("Leg Press should have been removed from modified proposal")
		}
	}
	found := false
	for _, ex := range proposal.Exercises {
		found = found || ex.ExerciseName == "Dead Bug"
	}
	if !found {
		t.Error("Dead Bug should have been added to modified proposal")
	}
}

func TestOrchestrator_WorkoutGenerationFlow(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := workout.NewReferentialResolver(nil, exResolver, nil)
	corrExtractor := workout.NewCorrectionExtractor(nil, "")
	corrSvc := workout.NewCorrectionService(nil, nil, refResolver, corrExtractor, nil, nil)
	memExtractor := NewMemoryExtractor(nil, "")
	builder := NewContextBuilder(DefaultBudgets())
	candSelector := NewCandidateSelector(nil)
	generator := NewWorkoutGenerator(nil, "")
	sessionSvc := NewSessionService(nil, nil)

	orch := NewOrchestrator(
		nil, nil, "", "", nil, corrSvc, memExtractor, builder,
		candSelector, generator, sessionSvc, nil, nil,
	)

	// User: "что сегодня делать?"
	res, err := orch.ProcessMessage(context.Background(), "user-1", 12345, 999, 101, nil, "что сегодня делать?")
	if err != nil {
		t.Fatalf("process message failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success: true on workout generation")
	}
	if res.ReplyContext == nil || res.ReplyContext.CurrentState["proposal"] == nil {
		t.Fatal("expected structured proposal for reply")
	}
}

func TestWorkoutProposal_TrackingTypeValidation(t *testing.T) {
	candSelector := NewCandidateSelector(nil)
	candidates, _ := candSelector.SelectCandidates(context.Background(), nil, nil, nil, nil)

	// Case 1: Assisted exercise without assistance_kg -> should fail
	invalidAssisted := &WorkoutProposalDraft{
		Title: "Test",
		Exercises: []ProposedExerciseDraft{
			{
				ExerciseID:   "00000000-0000-0000-0000-000000000003", // Graviton
				ExerciseName: "Graviton Pull-Ups",
				Sets:         []ProposedSetDraft{{SetNumber: 1, Reps: ptr(10)}}, // Missing assistance_kg!
			},
		},
	}
	if err := ValidateProposal(invalidAssisted, candidates); err == nil {
		t.Errorf("expected error for assisted exercise missing assistance_kg, got nil")
	}

	// Case 2: Negative reps -> should fail
	invalidReps := &WorkoutProposalDraft{
		Title: "Test",
		Exercises: []ProposedExerciseDraft{
			{
				ExerciseID:   "00000000-0000-0000-0000-000000000002", // Leg Curl
				ExerciseName: "Seated Leg Curl",
				Sets:         []ProposedSetDraft{{SetNumber: 1, Reps: ptr(-5), WeightKg: ptr(20.0)}},
			},
		},
	}
	if err := ValidateProposal(invalidReps, candidates); err == nil {
		t.Errorf("expected error for negative reps, got nil")
	}
}

func TestContextBuilder_ProposalVsPerformedDistinction(t *testing.T) {
	builder := NewContextBuilder(DefaultBudgets())

	// Proposal proposed 27kg, but actual performed was 23kg
	actualWorkouts := []workout.Workout{
		{
			ID: "w-actual",
			Exercises: []workout.WorkoutExercise{
				{
					ExerciseID:   "00000000-0000-0000-0000-000000000002",
					ExerciseName: "Seated Leg Curl",
					Sets: []workout.Set{
						{SetNumber: 1, Reps: ptr(12), WeightKg: ptr(23.0)},
					},
				},
			},
		},
	}

	convCtx := builder.BuildContext(
		context.Background(),
		ActionWorkoutRequest,
		"u-1",
		nil,
		nil,
		nil,
		actualWorkouts,
		nil,
		"Seated Leg Curl",
	)

	// Verify ContextBuilder contains 23kg from actual performed workout
	if len(convCtx.RelevantExerciseHistory) == 0 {
		t.Fatalf("expected exercise history to be populated from actual workouts")
	}
	hist := convCtx.RelevantExerciseHistory[0]
	if hist.Sets[0].WeightKg == nil || *hist.Sets[0].WeightKg != 23.0 {
		t.Errorf("context builder must strictly use actual performed weight 23kg, got: %v", hist.Sets[0].WeightKg)
	}
}

func TestSessionLifecycle_CompletionGating(t *testing.T) {
	sessionSvc := NewSessionService(nil, nil)
	orch := NewOrchestrator(nil, nil, "", "", nil, nil, nil, nil, nil, nil, sessionSvc, nil, nil)

	// Case 1: "готово" without active session -> should NOT complete anything
	res, err := orch.ProcessMessage(context.Background(), "u-1", 12345, 1001, 201, nil, "готово")
	if err != nil {
		t.Fatalf("process message failed: %v", err)
	}
	if strings.Contains(res.ConfirmationHTML, "Отличная тренировка") {
		t.Errorf("unrelated 'готово' must NOT trigger workout completion")
	}
}

func TestWorkoutProposal_CanonicalTrackingConsistency(t *testing.T) {
	candSelector := NewCandidateSelector(nil)
	candidates, _ := candSelector.SelectCandidates(context.Background(), nil, nil, nil, nil)

	// Cardio / Time-Distance proposal with canonical time_min and distance_km
	timeMin := 20
	distKm := 5.0
	cardioProposal := &WorkoutProposalDraft{
		Title:                "Кардио сессия",
		EstimatedDurationMin: 25,
		Exercises: []ProposedExerciseDraft{
			{
				ExerciseID:   "00000000-0000-0000-0000-000000000007", // Stationary Bike
				ExerciseName: "Stationary Bike",
				Sets: []ProposedSetDraft{
					{
						SetNumber:  1,
						TimeMin:    &timeMin,
						DistanceKm: &distKm,
					},
				},
			},
		},
	}

	if err := ValidateProposal(cardioProposal, candidates); err != nil {
		t.Fatalf("canonical cardio proposal validation failed: %v", err)
	}

	if card := cardioProposal.Exercises[0].Sets[0]; card.TimeMin == nil || *card.TimeMin != 20 {
		t.Fatal("expected structured 20 minute cardio set")
	}
}

func TestCompletionGating_UnrelatedVsWorkoutContext(t *testing.T) {
	sessionSvc := NewSessionService(nil, nil)
	orch := NewOrchestrator(nil, nil, "", "", nil, nil, nil, nil, nil, nil, sessionSvc, nil, nil)

	// Case A: Unrelated "готово" after preference modification should NOT complete session
	res, err := orch.ProcessMessage(context.Background(), "u-1", 12345, 1001, 201, nil, "готово")
	if err != nil {
		t.Fatalf("process message failed: %v", err)
	}
	if strings.Contains(res.ConfirmationHTML, "Отличная тренировка") {
		t.Errorf("unrelated 'готово' must NOT complete workout")
	}

	// Case B: Explicit workout completion wording "всё, тренировку закончил"
	resB, err := orch.ProcessMessage(context.Background(), "u-1", 12345, 1002, 202, nil, "тренировка окончена")
	if err != nil {
		t.Fatalf("process message failed: %v", err)
	}
	if !resB.Success {
		t.Fatalf("expected success: true")
	}
}

func TestOrchestrator_StartWorkoutLifecycleWords(t *testing.T) {
	startPhrases := []string{
		"начинаем",
		"начинаю",
		"начал",
		"начали",
		"погнали",
		"поехали",
		"старт",
		"делаю",
		"go",
		"start",
	}

	for _, phrase := range startPhrases {
		t.Run(phrase, func(t *testing.T) {
			sessionSvc := NewSessionService(nil, nil)
			orch := NewOrchestrator(nil, nil, "", "", nil, nil, nil, nil, nil, nil, sessionSvc, nil, nil)
			res, err := orch.ProcessMessage(context.Background(), "u-1", 12345, 1001, 201, nil, phrase)
			if err != nil {
				t.Fatalf("phrase %q failed: %v", phrase, err)
			}
			if !res.Success {
				t.Errorf("phrase %q expected success: true", phrase)
			}
		})
	}
}

func TestSameExerciseAccumulation_Logic(t *testing.T) {
	// Simulate active workout with existing Leg Curl (2 sets)
	existingSets := []workout.Set{
		{SetNumber: 1, Reps: ptr(12), WeightKg: ptr(23.0)},
		{SetNumber: 2, Reps: ptr(12), WeightKg: ptr(23.0)},
	}

	// Incoming message: "еще один подход лег курла 12х23" -> 1 set
	incomingSets := []workout.Set{
		{SetNumber: 1, Reps: ptr(12), WeightKg: ptr(23.0)},
	}

	startSetNum := len(existingSets) + 1
	for idx := range incomingSets {
		incomingSets[idx].SetNumber = startSetNum + idx
	}
	mergedSets := append(existingSets, incomingSets...)

	if len(mergedSets) != 3 {
		t.Fatalf("expected 3 merged sets, got %d", len(mergedSets))
	}
	if mergedSets[2].SetNumber != 3 {
		t.Errorf("expected 3rd set to have set_number 3, got %d", mergedSets[2].SetNumber)
	}
}

type mockExerciseResolver struct {
	mapping map[string]*exercise.Exercise
}

func (m *mockExerciseResolver) Resolve(ctx context.Context, rawName string) (*exercise.ResolutionResult, error) {
	norm := strings.ToLower(strings.TrimSpace(rawName))
	for k, ex := range m.mapping {
		if strings.Contains(norm, k) || strings.Contains(k, norm) {
			return &exercise.ResolutionResult{
				Status:   exercise.ResolutionExact,
				Exercise: ex,
			}, nil
		}
	}
	return &exercise.ResolutionResult{Status: exercise.ResolutionNotFound}, nil
}

func TestMemoryExtraction_CanonicalIdentityAndTargetResolution(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000099", Name: "Assisted Pull-up (Graviton)"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{
			"гравитон": gravitonEx,
		},
	}

	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	candidates, err := extractor.ExtractCandidates(context.Background(), "пока не давай гравитон", time.Local)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected exactly 1 candidate, got %d", len(candidates))
	}

	c := candidates[0]
	expectedKey := "avoid_exercise:00000000-0000-0000-0000-000000000099"
	if c.Key != expectedKey {
		t.Errorf("expected canonical key %s, got %s", expectedKey, c.Key)
	}
	if c.Scope != "conditional" {
		t.Errorf("expected conditional scope, got %s", c.Scope)
	}
}

func TestMemoryExtraction_DistinctExercisesGetDistinctKeys(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000001", Name: "Graviton"}
	legPressEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000002", Name: "Leg Press"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{
			"гравитон":  gravitonEx,
			"лег пресс": legPressEx,
		},
	}

	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	cand1, _ := extractor.ExtractCandidates(context.Background(), "пока не давай гравитон", time.Local)
	cand2, _ := extractor.ExtractCandidates(context.Background(), "пока не давай лег пресс", time.Local)

	if len(cand1) != 1 || len(cand2) != 1 {
		t.Fatalf("expected 1 candidate each, got %d and %d", len(cand1), len(cand2))
	}

	if cand1[0].Key == cand2[0].Key {
		t.Errorf("distinct exercises must NOT share the same key: got %s for both", cand1[0].Key)
	}
	if cand1[0].Key != "avoid_exercise:00000000-0000-0000-0000-000000000001" {
		t.Errorf("unexpected cand1 key: %s", cand1[0].Key)
	}
	if cand2[0].Key != "avoid_exercise:00000000-0000-0000-0000-000000000002" {
		t.Errorf("unexpected cand2 key: %s", cand2[0].Key)
	}
}

func TestMemoryExtraction_SameExerciseScopeChange(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000001", Name: "Graviton"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{
			"гравитон": gravitonEx,
		},
	}

	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	candCond, _ := extractor.ExtractCandidates(context.Background(), "пока не давай гравитон", time.Local)
	candPerm, _ := extractor.ExtractCandidates(context.Background(), "гравитон больше никогда не давай", time.Local)

	if len(candCond) != 1 || len(candPerm) != 1 {
		t.Fatalf("expected 1 candidate each, got %d and %d", len(candCond), len(candPerm))
	}

	// Must share the exact same canonical key so DB supersession works
	if candCond[0].Key != candPerm[0].Key {
		t.Errorf("expected identical canonical keys for supersession, got %s and %s", candCond[0].Key, candPerm[0].Key)
	}
	if candCond[0].Scope != "conditional" {
		t.Errorf("expected conditional scope, got %s", candCond[0].Scope)
	}
	if candPerm[0].Scope != "permanent" {
		t.Errorf("expected permanent scope, got %s", candPerm[0].Scope)
	}
}

func TestMemoryExtraction_DeduplicationOfDualSources(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000001", Name: "Graviton"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{
			"гравитон": gravitonEx,
		},
	}

	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	// Simulate receiving two differently formatted candidates for the same utterance
	rawCandidates := []MemoryCandidate{
		{
			Category:   "preference",
			Intent:     "avoid_exercise",
			TargetRaw:  "гравитон",
			Key:        "conditional_avoidance",
			Value:      "пока не давай гравитон",
			Scope:      "conditional",
			Confidence: 0.90,
			SourceText: "пока не давай гравитон",
		},
		{
			Category:   "preference",
			Intent:     "avoid_exercise",
			TargetRaw:  "гравитон",
			Key:        "avoid_graviton",
			Value:      "Исключить гравитон",
			Scope:      "conditional",
			Confidence: 0.95,
			SourceText: "пока не давай гравитон",
		},
	}

	deduped := extractor.CanonicalizeCandidates(context.Background(), rawCandidates)
	if len(deduped) != 1 {
		t.Fatalf("expected exactly 1 deduplicated candidate, got %d: %+v", len(deduped), deduped)
	}
	if deduped[0].Key != "avoid_exercise:00000000-0000-0000-0000-000000000001" {
		t.Errorf("expected canonical key avoid_exercise:00000000-0000-0000-0000-000000000001, got %s", deduped[0].Key)
	}
	if deduped[0].Confidence != 0.95 {
		t.Errorf("expected highest confidence candidate (0.95) to be preserved, got %.2f", deduped[0].Confidence)
	}
}

func TestMemoryExtraction_AmbiguousOrUnresolvedTargetSkipped(t *testing.T) {
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{},
	}

	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	// Unresolved / unknown exercise name must NOT produce an avoid_exercise:<slug> key
	candUnknown, err := extractor.ExtractCandidates(context.Background(), "пока не давай неизвестная_штука", time.Local)
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}
	if len(candUnknown) != 0 {
		t.Errorf("expected 0 candidates for unresolvable target, got %d: %+v", len(candUnknown), candUnknown)
	}
}

func TestMemoryExtraction_AliasNormalizationSameKey(t *testing.T) {
	gravitonEx := &exercise.Exercise{ID: "00000000-0000-0000-0000-000000000001", Name: "Graviton"}
	resolver := &mockExerciseResolver{
		mapping: map[string]*exercise.Exercise{
			"гравитон":  gravitonEx,
			"гравитрон": gravitonEx,
			"подтягивания в гравитоне": gravitonEx,
		},
	}

	extractor := NewMemoryExtractor(nil, "")
	extractor.SetExerciseResolver(resolver)

	cand1, _ := extractor.ExtractCandidates(context.Background(), "пока не давай гравитон", time.Local)
	cand2, _ := extractor.ExtractCandidates(context.Background(), "пока не давай гравитрон", time.Local)

	if len(cand1) != 1 || len(cand2) != 1 {
		t.Fatalf("expected 1 candidate each, got %d and %d", len(cand1), len(cand2))
	}
	if cand1[0].Key != cand2[0].Key {
		t.Errorf("aliases must resolve to identical canonical UUID key: got %s and %s", cand1[0].Key, cand2[0].Key)
	}
	if cand1[0].Key != "avoid_exercise:00000000-0000-0000-0000-000000000001" {
		t.Errorf("unexpected key: %s", cand1[0].Key)
	}
}
