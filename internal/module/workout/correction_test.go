package workout

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
)

func TestCorrectionExtraction_FastAndDirect(t *testing.T) {
	extractor := NewCorrectionExtractor(nil, "")

	// 1. Direct weight correction "не 23, а 27"
	d1, err := extractor.ExtractCorrectionDraft(context.Background(), "не 23, а 27")
	if err != nil || d1 == nil || !d1.IsCorrection {
		t.Fatalf("failed to fast-extract 'не 23, а 27': %v, draft: %+v", err, d1)
	}
	if len(d1.Changes) != 1 || d1.Changes[0].Field != "weight_kg" || *d1.Changes[0].NewValue != 27.0 {
		t.Errorf("unexpected change extracted: %+v", d1.Changes)
	}

	// 2. Set count correction "там было 2 подхода, не 3"
	d2, err := extractor.ExtractCorrectionDraft(context.Background(), "там было 2 подхода, не 3")
	if err != nil || d2 == nil || !d2.IsCorrection {
		t.Fatalf("failed to extract set count correction: %v", err)
	}
	if d2.Changes[0].Field != "set_count" || *d2.Changes[0].NewValue != 2.0 {
		t.Errorf("unexpected set count change: %+v", d2.Changes[0])
	}

	// 3. Assistance weight correction "ассист был 36, не 32"
	d3, err := extractor.ExtractCorrectionDraft(context.Background(), "ассист был 36, не 32")
	if err != nil || d3 == nil || !d3.IsCorrection {
		t.Fatalf("failed to extract assistance change: %v", err)
	}
	if d3.Changes[0].Field != "assistance_kg" || *d3.Changes[0].NewValue != 36.0 {
		t.Errorf("unexpected assistance change: %+v", d3.Changes[0])
	}

	// 4. Added machine weight "+15 к тренажеру, не +10"
	d4, err := extractor.ExtractCorrectionDraft(context.Background(), "+15 к тренажеру, не +10")
	if err != nil || d4 == nil || !d4.IsCorrection {
		t.Fatalf("failed to extract added weight change: %v", err)
	}
	if d4.Changes[0].Field != "added_weight_kg" || *d4.Changes[0].NewValue != 15.0 {
		t.Errorf("unexpected added weight change: %+v", d4.Changes[0])
	}

	// 5. Specific set reps "во втором подходе 10 повторов"
	d5, err := extractor.ExtractCorrectionDraft(context.Background(), "во втором подходе 10 повторов")
	if err != nil || d5 == nil || !d5.IsCorrection {
		t.Fatalf("failed to extract specific set reps: %v", err)
	}
	if d5.Changes[0].Field != "reps" || *d5.Changes[0].NewValue != 10.0 || *d5.Changes[0].TargetSet != 2 {
		t.Errorf("unexpected specific set change: %+v", d5.Changes[0])
	}
}

func TestCorrectionExtraction_HypotheticalAndQuestionsIgnored(t *testing.T) {
	extractor := NewCorrectionExtractor(nil, "")

	nonCorrections := []string{
		"может в следующий раз 27?",
		"а если поставить 27 будет норм?",
		"почему ты дал 23?",
		"как правильно делать дед баг?",
	}

	for _, text := range nonCorrections {
		d, err := extractor.ExtractCorrectionDraft(context.Background(), text)
		if err != nil {
			t.Fatalf("unexpected error on %q: %v", text, err)
		}
		if d.IsCorrection {
			t.Errorf("hypothetical/question %q must NOT be extracted as a correction", text)
		}
	}
}

type mockTelegramRepoForResolver struct {
	messages map[int64]*telegram.TelegramMessage
}

func (m *mockTelegramRepoForResolver) GetMessage(ctx context.Context, chatID int64, messageID int64) (*telegram.TelegramMessage, error) {
	if m != nil && m.messages != nil {
		return m.messages[messageID], nil
	}
	return nil, nil
}

func (m *mockTelegramRepoForResolver) GetConversationState(ctx context.Context, userID string) (*telegram.ConversationState, error) {
	return nil, nil
}

func TestReferentialResolver_ReplyContext(t *testing.T) {
	msgStore := make(map[int64]*telegram.TelegramMessage)
	msgStore[2002] = &telegram.TelegramMessage{
		ChatID:    12345,
		MessageID: 2002,
		UserID:    "user-1",
		Direction: telegram.DirectionOutbound,
		DomainReferences: []telegram.DomainReference{
			{Type: "workout", ID: "w-reply-100"},
			{Type: "exercise", ID: "ex-leg-curl", Name: "Leg Curl"},
		},
	}

	mockRepo := &mockTelegramRepoForResolver{messages: msgStore}
	resolver := NewReferentialResolver(nil, nil, mockRepo)

	target, err := resolver.resolveByReplyContext(context.Background(), 12345, 2002, "user-1")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if target == nil || target.WorkoutID != "w-reply-100" || target.ExerciseID != "ex-leg-curl" {
		t.Fatalf("expected target w-reply-100 / ex-leg-curl, got %+v", target)
	}
}

func TestCorrectionService_DirectContextualWeightPatch(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := NewReferentialResolver(nil, exResolver, nil)
	extractor := NewCorrectionExtractor(nil, "")
	corrSvc := NewCorrectionService(nil, nil, refResolver, extractor, nil, nil)

	// User: "не 23, а 27"
	res, err := corrSvc.ProcessCorrection(context.Background(), "user-1", 12345, 101, 555, nil, "не 23, а 27")
	if err != nil {
		t.Fatalf("correction failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success: true on correction")
	}
	if sets := correctedSets(t, res); sets[0].WeightKg == nil || *sets[0].WeightKg != 27 {
		t.Errorf("expected corrected weight 27 kg, got: %+v", sets[0])
	}
	if len(res.DomainReferences) == 0 {
		t.Errorf("expected domain references in confirmation")
	}
}

func TestCorrectionService_IndividualSetPatch(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := NewReferentialResolver(nil, exResolver, nil)
	extractor := NewCorrectionExtractor(nil, "")
	corrSvc := NewCorrectionService(nil, nil, refResolver, extractor, nil, nil)

	// User: "во втором подходе 10 повторов"
	res, err := corrSvc.ProcessCorrection(context.Background(), "user-1", 12345, 102, 556, nil, "во втором подходе 10 повторов")
	if err != nil {
		t.Fatalf("correction failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success: true")
	}
	if sets := correctedSets(t, res); sets[1].Reps == nil || *sets[1].Reps != 10 {
		t.Errorf("expected 10 reps in second set, got: %+v", sets[1])
	}
}

func TestCorrectionService_SetCountCorrection(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := NewReferentialResolver(nil, exResolver, nil)
	extractor := NewCorrectionExtractor(nil, "")
	corrSvc := NewCorrectionService(nil, nil, refResolver, extractor, nil, nil)

	// User: "там было 2 подхода, не 3"
	res, err := corrSvc.ProcessCorrection(context.Background(), "user-1", 12345, 103, 557, nil, "там было 2 подхода, не 3")
	if err != nil {
		t.Fatalf("correction failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success: true")
	}
	if sets := correctedSets(t, res); len(sets) != 2 {
		t.Errorf("expected two sets after correction, got: %+v", sets)
	}
}

func TestCorrectionService_MachineAddedWeightCorrection(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := NewReferentialResolver(nil, exResolver, nil)
	extractor := NewCorrectionExtractor(nil, "")
	corrSvc := NewCorrectionService(nil, nil, refResolver, extractor, nil, nil)

	// User: "+15 к тренажеру, не +10"
	res, err := corrSvc.ProcessCorrection(context.Background(), "user-1", 12345, 104, 558, nil, "+15 к тренажеру, не +10")
	if err != nil {
		t.Fatalf("correction failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success: true")
	}
	if sets := correctedSets(t, res); sets[0].AddedWeightKg == nil || *sets[0].AddedWeightKg != 15 {
		t.Errorf("expected added weight 15 kg, got: %+v", sets[0])
	}
}

func TestCorrectionClarificationContinuation(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := NewReferentialResolver(nil, exResolver, nil)
	extractor := NewCorrectionExtractor(nil, "")
	corrSvc := NewCorrectionService(nil, nil, refResolver, extractor, nil, nil)

	draft := CorrectionDraft{
		IsCorrection: true,
		Changes: []FieldChange{
			{Field: "weight_kg", OldValue: ptr(23.0), NewValue: ptr(27.0), SetSelector: "all"},
		},
	}
	ctxBytes, _ := json.Marshal(CorrectionContextData{Draft: draft})

	pendingClar := &telegram.PendingClarification{
		Type:          "correction_disambiguation",
		OriginalQuery: "не 23, а 27",
		Candidates:    []string{"Leg Curl", "Chest Press"},
		Context:       ctxBytes,
		CreatedAt:     time.Now(),
	}

	// User clarifies: "в лег курле"
	res, err := corrSvc.continueCorrectionClarification(context.Background(), "u-1", 12345, 999, 101, "в лег курле", pendingClar)
	if err != nil {
		t.Fatalf("clarification continuation failed: %v", err)
	}

	if res.NeedsClarify {
		t.Errorf("expected clarification to be resolved, got needs_clarify: true")
	}
	if !res.Success {
		t.Errorf("expected success: true on clarified correction")
	}
	if sets := correctedSets(t, res); sets[0].WeightKg == nil || *sets[0].WeightKg != 27 {
		t.Errorf("expected corrected weight 27 kg, got: %+v", sets[0])
	}
}

func TestCorrectionService_IncreasingSetCountAsksClarification(t *testing.T) {
	exResolver := exercise.NewResolver(nil, nil, "")
	refResolver := NewReferentialResolver(nil, exResolver, nil)
	extractor := NewCorrectionExtractor(nil, "")
	corrSvc := NewCorrectionService(nil, nil, refResolver, extractor, nil, nil)

	// User: "там было 4 подхода, не 3" (missing weight and reps for the 4th set)
	draft := &CorrectionDraft{
		IsCorrection: true,
		Changes: []FieldChange{
			{Field: "set_count", OldValue: ptr(3.0), NewValue: ptr(4.0)},
		},
	}
	target := &ResolvedTarget{
		WorkoutID:         "w-100",
		WorkoutExerciseID: "we-100",
		ExerciseID:        "ex-1",
		ExerciseName:      "Leg Curl",
	}

	res, err := corrSvc.applyPatchToWorkout(context.Background(), "user-1", 12345, 105, 559, target, draft)
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}

	if !res.NeedsClarify {
		t.Fatalf("increasing set count without reps/weight MUST ask for clarification, got needs_clarify: false")
	}
	if res.ReplyContext == nil || res.ReplyContext.EventType != "missing_set_details" || res.ReplyContext.CurrentState["set_number"] != 4 {
		t.Errorf("expected structured clarification for fourth set, got: %+v", res.ReplyContext)
	}
}

func TestCorrectionSafety_CrossUserIsolation(t *testing.T) {
	resolver := NewReferentialResolver(nil, nil, nil)

	// Attempting to resolve a reply that belongs to a different user
	target, err := resolver.resolveByReplyContext(context.Background(), 12345, 999, "different-user")
	if target != nil {
		t.Errorf("must not resolve target from a different user's message")
	}
	_ = err
}

func correctedSets(t *testing.T, res *telegram.IngestionResponse) []Set {
	t.Helper()
	if res.ReplyContext == nil || res.ReplyContext.EventType != "workout_corrected" {
		t.Fatalf("expected structured workout correction, got: %+v", res.ReplyContext)
	}
	ex, ok := res.ReplyContext.CurrentState["corrected_exercise"].(*WorkoutExercise)
	if !ok || ex == nil {
		t.Fatalf("corrected exercise missing: %+v", res.ReplyContext.CurrentState)
	}
	return ex.Sets
}
