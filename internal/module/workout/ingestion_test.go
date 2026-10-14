package workout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
)

func mockOpenRouterServer(t *testing.T, responseJSON string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		type choice struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}
		type openRouterResp struct {
			Choices []choice `json:"choices"`
		}

		resp := openRouterResp{
			Choices: []choice{
				{
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: responseJSON,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestWorkoutDraftExtraction_StandardWeighted(t *testing.T) {
	mockJSON := `{
		"workout_type": "strength",
		"exercises": [
			{
				"raw_name": "лег курл",
				"sets": [
					{"set_number": 1, "reps": 12, "weight_kg": 23.0},
					{"set_number": 2, "reps": 12, "weight_kg": 23.0},
					{"set_number": 3, "reps": 12, "weight_kg": 23.0}
				]
			}
		]
	}`

	server := mockOpenRouterServer(t, mockJSON)
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("mock-key", "mock-model", server.URL, server.Client())
	extractor := NewExtractor(client, "mock-model")

	draft, err := extractor.ExtractWorkoutDraft(context.Background(), "лег курл 3х12 23кг")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(draft.Exercises) != 1 {
		t.Fatalf("expected 1 exercise, got %d", len(draft.Exercises))
	}
	ex := draft.Exercises[0]
	if ex.RawName != "лег курл" {
		t.Errorf("expected raw_name 'лег курл', got %q", ex.RawName)
	}
	if len(ex.Sets) != 3 {
		t.Fatalf("expected 3 sets, got %d", len(ex.Sets))
	}
	for i, s := range ex.Sets {
		if s.SetNumber != i+1 {
			t.Errorf("expected set number %d, got %d", i+1, s.SetNumber)
		}
		if s.Reps == nil || *s.Reps != 12 {
			t.Errorf("expected 12 reps in set %d, got %v", i+1, s.Reps)
		}
		if s.WeightKg == nil || *s.WeightKg != 23.0 {
			t.Errorf("expected 23.0 kg in set %d, got %v", i+1, s.WeightKg)
		}
	}
}

func TestWorkoutDraftExtraction_MachineBaseAndAdded(t *testing.T) {
	mockJSON := `{
		"workout_type": "strength",
		"exercises": [
			{
				"raw_name": "хип траст",
				"sets": [
					{"set_number": 1, "reps": 15, "base_weight_kg": 22.7, "added_weight_kg": 10.0},
					{"set_number": 2, "reps": 15, "base_weight_kg": 22.7, "added_weight_kg": 10.0},
					{"set_number": 3, "reps": 15, "base_weight_kg": 22.7, "added_weight_kg": 10.0}
				]
			}
		]
	}`

	server := mockOpenRouterServer(t, mockJSON)
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("mock-key", "mock-model", server.URL, server.Client())
	extractor := NewExtractor(client, "mock-model")

	draft, err := extractor.ExtractWorkoutDraft(context.Background(), "хип траст 3х15 10кг + тренажер 22.7")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	ex := draft.Exercises[0]
	if len(ex.Sets) != 3 {
		t.Fatalf("expected 3 sets, got %d", len(ex.Sets))
	}
	s1 := ex.Sets[0]
	if s1.BaseWeightKg == nil || *s1.BaseWeightKg != 22.7 {
		t.Errorf("expected base weight 22.7, got %v", s1.BaseWeightKg)
	}
	if s1.AddedWeightKg == nil || *s1.AddedWeightKg != 10.0 {
		t.Errorf("expected added weight 10.0, got %v", s1.AddedWeightKg)
	}
	if s1.WeightKg != nil {
		t.Errorf("weight_kg should be nil to preserve distinct components, got %v", *s1.WeightKg)
	}
}

func TestWorkoutDraftExtraction_AssistedExercise(t *testing.T) {
	mockJSON := `{
		"workout_type": "strength",
		"exercises": [
			{
				"raw_name": "гравитон",
				"difficulty": "hard",
				"feedback_raw": "тяжело",
				"sets": [
					{"set_number": 1, "reps": 10, "assistance_kg": 32.0},
					{"set_number": 2, "reps": 10, "assistance_kg": 32.0}
				]
			}
		]
	}`

	server := mockOpenRouterServer(t, mockJSON)
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("mock-key", "mock-model", server.URL, server.Client())
	extractor := NewExtractor(client, "mock-model")

	draft, err := extractor.ExtractWorkoutDraft(context.Background(), "гравитон 2х10 ассист 32 тяжело")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	ex := draft.Exercises[0]
	if ex.Difficulty != "hard" {
		t.Errorf("expected difficulty 'hard', got %q", ex.Difficulty)
	}
	if ex.FeedbackRaw != "тяжело" {
		t.Errorf("expected feedback_raw 'тяжело', got %q", ex.FeedbackRaw)
	}
	s := ex.Sets[0]
	if s.AssistanceKg == nil || *s.AssistanceKg != 32.0 {
		t.Errorf("expected assistance_kg 32.0, got %v", s.AssistanceKg)
	}
	if s.WeightKg != nil {
		t.Errorf("assisted exercise must not invent fake weight_kg, got %v", *s.WeightKg)
	}
}

func TestWorkoutDraftExtraction_MultiExerciseOrder(t *testing.T) {
	mockJSON := `{
		"workout_type": "strength",
		"exercises": [
			{
				"raw_name": "лег курл",
				"difficulty": "easy",
				"sets": [{"set_number": 1, "reps": 12, "weight_kg": 23.0}]
			},
			{
				"raw_name": "хип траст",
				"sets": [{"set_number": 1, "reps": 15, "base_weight_kg": 22.7, "added_weight_kg": 10.0}]
			},
			{
				"raw_name": "гравитон",
				"difficulty": "hard",
				"sets": [{"set_number": 1, "reps": 10, "assistance_kg": 32.0}]
			}
		]
	}`

	server := mockOpenRouterServer(t, mockJSON)
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("mock-key", "mock-model", server.URL, server.Client())
	extractor := NewExtractor(client, "mock-model")

	draft, err := extractor.ExtractWorkoutDraft(context.Background(), "лег курл 3х12 23 легко, хип траст 3х15 +10 к тренажеру 22.7, гравитон 2х10 ассист 32 последний тяжело")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	if len(draft.Exercises) != 3 {
		t.Fatalf("expected 3 exercises, got %d", len(draft.Exercises))
	}
	if draft.Exercises[0].RawName != "лег курл" || draft.Exercises[1].RawName != "хип траст" || draft.Exercises[2].RawName != "гравитон" {
		t.Errorf("exercises not extracted in correct sequential order")
	}
}

func TestWorkoutDraftExtraction_InvalidLLMOutputFailsSafely(t *testing.T) {
	server := mockOpenRouterServer(t, "This is not valid JSON at all")
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("mock-key", "mock-model", server.URL, server.Client())
	extractor := NewExtractor(client, "mock-model")

	_, err := extractor.ExtractWorkoutDraft(context.Background(), "жим 100")
	if err == nil {
		t.Error("expected error on malformed non-JSON LLM output")
	}
}

func TestNonWorkoutMessagesDoNotMutateHistory(t *testing.T) {
	nonWorkoutQueries := []string{
		"что сегодня делать?",
		"как у меня прогресс?",
		"привет",
		"что я делал вчера?",
		"как правильно делать дед баг?",
		"составь мне тренировку на 45 минут",
	}

	for _, query := range nonWorkoutQueries {
		if IsPlausibleWorkoutLog(query) {
			t.Errorf("expected %q to be classified as conversational, not a workout log", query)
		}
	}

	actualWorkoutLogs := []string{
		"лег курл 3х12 23кг",
		"жим лежа 4х8 80кг",
		"хип траст 3х15 10кг + тренажер 22.7",
		"гравитон 2х10 ассист 32 тяжело",
		"присед 3 по 10 90кг",
	}

	for _, log := range actualWorkoutLogs {
		if !IsPlausibleWorkoutLog(log) {
			t.Errorf("expected %q to be classified as a plausible workout log", log)
		}
	}
}

func TestExerciseResolutionAndAmbiguity(t *testing.T) {
	res := exercise.NewResolver(nil, nil, "")

	// 1. Exact / Known slang
	r1, err := res.Resolve(context.Background(), "лег курл")
	if err != nil || r1.Status != exercise.ResolutionExact || r1.Exercise == nil {
		t.Errorf("failed to resolve 'лег курл': %v, result: %+v", err, r1)
	}

	// 2. Ambiguous query without qualifiers
	r2, err := res.Resolve(context.Background(), "сгибание ног")
	if err != nil || r2.Status != exercise.ResolutionAmbiguous {
		t.Errorf("expected ambiguous status for unqualified 'сгибание ног', got status: %v", r2.Status)
	}
	if len(r2.Candidates) != 2 {
		t.Errorf("expected two exercise choices, got: %+v", r2.Candidates)
	}
}

func TestClarificationContinuationFlow(t *testing.T) {
	res := exercise.NewResolver(nil, nil, "")
	pipeline := NewIngestionPipeline(nil, nil, res, nil, nil, nil)

	reps := 12
	weight := 23.0
	preservedDraft := WorkoutDraft{
		WorkoutType: "strength",
		Exercises: []WorkoutDraftExercise{
			{
				RawName: "сгибание ног",
				Sets: []WorkoutDraftSet{
					{SetNumber: 1, Reps: &reps, WeightKg: &weight},
					{SetNumber: 2, Reps: &reps, WeightKg: &weight},
					{SetNumber: 3, Reps: &reps, WeightKg: &weight},
				},
			},
		},
	}
	ctxBytes, _ := json.Marshal(ClarificationContextData{
		Draft:         preservedDraft,
		ExerciseIndex: 0,
		RawName:       "сгибание ног",
	})

	pendingClar := &telegram.PendingClarification{
		Type:          "exercise_disambiguation",
		OriginalQuery: "сгибание ног",
		Candidates: []string{
			"Сгибание ног сидя (Seated Leg Curl)",
			"Сгибание ног лежа (Lying Leg Curl)",
		},
		Context: ctxBytes,
	}

	// User answers "сидя"
	resp, err := pipeline.ContinueClarification(context.Background(), "u-1", 12345, 999, 101, "сидя", pendingClar)
	if err != nil {
		t.Fatalf("clarification continuation failed: %v", err)
	}

	if resp.NeedsClarify {
		t.Errorf("expected clarification to be resolved, got needs_clarify: true")
	}
	if !resp.Success {
		t.Errorf("expected success: true on clarified draft commit")
	}
	if resp.ReplyContext == nil || resp.ReplyContext.EventType != "workout_logged" {
		t.Fatalf("expected structured workout log, got: %+v", resp.ReplyContext)
	}
	inputs, ok := resp.ReplyContext.CurrentState["added_exercises"].([]WorkoutExerciseInput)
	if !ok || len(inputs) != 1 || inputs[0].ExerciseName == "" {
		t.Errorf("expected resolved exercise in reply context, got: %+v", resp.ReplyContext.CurrentState)
	}
	if len(resp.DomainReferences) == 0 {
		t.Errorf("expected domain references in confirmation response")
	}
}

func TestConstrainedLLMResolution_RejectsInvalidCandidate(t *testing.T) {
	// Mock server returns a non-existent fake ID
	server := mockOpenRouterServer(t, `{"id": "hallucinated-fake-uuid-999"}`)
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("mock-key", "mock-model", server.URL, server.Client())
	res := exercise.NewResolver(nil, client, "mock-model")

	// When candidate set does NOT contain the fake ID, it must return Ambiguous / Not Found
	result, err := res.Resolve(context.Background(), "неизвестное редкое упражнение")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if result.Status == exercise.ResolutionLLMSelected {
		t.Errorf("must not accept hallucinated candidate ID")
	}
}
