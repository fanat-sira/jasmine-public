package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/pkg/correlation"
	"jasmine/internal/pkg/identity"
)

type mockLLMRecorder struct {
	records []openrouter.LLMCallRecord
}

func (m *mockLLMRecorder) RecordLLMCall(ctx context.Context, record openrouter.LLMCallRecord) {
	m.records = append(m.records, record)
}

func TestRuntime_CorrelationPropagationThroughContext(t *testing.T) {
	corrID := correlation.GenerateUpdateCorrelationID(777123)
	ctx := correlation.WithCorrelationID(context.Background(), corrID)
	ctx = identity.WithUserID(ctx, "user-999")
	ctx = correlation.WithOperation(ctx, "workout_draft_extraction")

	recorder := &mockLLMRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": `{"workout_type":"strength","exercises":[{"raw_name":"жим гантелей","sets":[{"set_number":1,"reps":10,"weight_kg":24}]}]}`,
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     45,
				"completion_tokens": 30,
				"total_tokens":      75,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("test-key", "test-model", server.URL, server.Client())
	client.SetObserver(recorder)

	out, err := client.SendChat(ctx, "test-model", "жим гантелей 1х10 24кг")
	if err != nil {
		t.Fatalf("unexpected SendChat error: %v", err)
	}

	if !strings.Contains(out, "жим гантелей") {
		t.Errorf("unexpected output: %s", out)
	}

	if len(recorder.records) != 1 {
		t.Fatalf("expected 1 LLM record, got %d", len(recorder.records))
	}

	rec := recorder.records[0]
	if rec.CorrelationID != corrID {
		t.Errorf("expected correlation ID %s, got %s", corrID, rec.CorrelationID)
	}
	if rec.UserID != "user-999" {
		t.Errorf("expected user ID user-999, got %s", rec.UserID)
	}
	if rec.Operation != "workout_draft_extraction" {
		t.Errorf("expected operation workout_draft_extraction, got %s", rec.Operation)
	}
	if !rec.Success {
		t.Errorf("expected Success=true")
	}
	if !rec.StructuredOutputValid {
		t.Errorf("expected StructuredOutputValid=true")
	}
	if rec.InputTokens == nil || *rec.InputTokens != 45 {
		t.Errorf("expected input tokens 45, got %v", rec.InputTokens)
	}
}

func TestRuntime_SeparateTransportSuccessFromInvalidStructuredOutput(t *testing.T) {
	corrID := "corr-test-invalid-json"
	ctx := correlation.WithCorrelationID(context.Background(), corrID)
	ctx = identity.WithUserID(ctx, "user-888")
	ctx = correlation.WithOperation(ctx, "workout_draft_extraction")

	recorder := &mockLLMRecorder{}

	// Server returns HTTP 200 with invalid schema/non-JSON content
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "This is conversational filler that is not JSON!",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("test-key", "test-model", server.URL, server.Client())
	client.SetObserver(recorder)

	raw, err := client.SendChat(ctx, "test-model", "test prompt")
	if err != nil {
		t.Fatalf("unexpected SendChat error: %v", err)
	}

	// Simulated downstream extractor validation failure
	valErr := errors.New("response is not valid JSON")
	client.RecordStructuredValidation(ctx, "workout_draft_extraction", "test-model", 120, false, valErr)

	if len(recorder.records) < 2 {
		t.Fatalf("expected at least 2 records (transport + structured validation), got %d", len(recorder.records))
	}

	transportRec := recorder.records[0]
	if !transportRec.Success {
		t.Errorf("expected transport success=true")
	}

	valRec := recorder.records[1]
	if !valRec.Success {
		t.Errorf("expected valRec.Success=true (provider HTTP call succeeded)")
	}
	if valRec.StructuredOutputValid {
		t.Errorf("expected valRec.StructuredOutputValid=false")
	}
	if valRec.ErrorMessage == nil || !strings.Contains(*valRec.ErrorMessage, "not valid JSON") {
		t.Errorf("expected validation error message, got %v", valRec.ErrorMessage)
	}
	if valRec.CorrelationID != corrID {
		t.Errorf("expected correlation preserved on validation error: %s", valRec.CorrelationID)
	}
	_ = raw
}

func TestRuntime_ProviderFailureLoggedWithCorrelation(t *testing.T) {
	corrID := "corr-test-provider-fail"
	ctx := correlation.WithCorrelationID(context.Background(), corrID)
	ctx = identity.WithUserID(ctx, "user-777")
	ctx = correlation.WithOperation(ctx, "workout_generation")

	recorder := &mockLLMRecorder{}

	// Server returns HTTP 502 Bad Gateway
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"provider upstream unavailable"}}`))
	}))
	defer server.Close()

	client := openrouter.NewClientWithBaseURL("test-key", "test-model", server.URL, server.Client())
	client.SetObserver(recorder)

	_, err := client.SendChat(ctx, "test-model", "generate workout")
	if err == nil {
		t.Fatalf("expected error from 502 Bad Gateway")
	}

	if len(recorder.records) != 1 {
		t.Fatalf("expected 1 failure log, got %d", len(recorder.records))
	}

	rec := recorder.records[0]
	if rec.Success {
		t.Errorf("expected Success=false on provider failure")
	}
	if rec.StructuredOutputValid {
		t.Errorf("expected StructuredOutputValid=false")
	}
	if rec.CorrelationID != corrID {
		t.Errorf("expected correlation preserved on failure: got %s", rec.CorrelationID)
	}
	if rec.ErrorMessage == nil || !strings.Contains(*rec.ErrorMessage, "provider upstream unavailable") {
		t.Errorf("expected error message logged, got %v", rec.ErrorMessage)
	}
}
