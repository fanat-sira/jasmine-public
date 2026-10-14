package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/prompts"
)

type personalFactPromptStore map[string]string

func (s personalFactPromptStore) GetPromptTemplate(_ context.Context, key string) (string, error) {
	return s[key], nil
}

func TestPersonalKnowledgeExtractCombinesExplicitFactsAndPromptOverride(t *testing.T) {
	var receivedPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []openrouter.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Messages) > 0 {
			receivedPrompt, _ = request.Messages[0].Content.(string)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"facts":[{"namespace":"project","subject":"jasmine","predicate":"name","value":"Jasmine","operation":"replace","confidence":1,"evidence":"делаю проект Jasmine"}]}`}}}})
	}))
	defer server.Close()

	knowledge := NewPersonalKnowledge(nil, openrouter.NewClientWithBaseURL("test", "memory-model", server.URL, server.Client()), "memory-model")
	knowledge.SetPromptStore(personalFactPromptStore{prompts.KeyPersonalFactExtraction: "MEMORY_OVERRIDE"})
	facts, err := knowledge.Extract(context.Background(), "я живу в Москве и делаю проект Jasmine")
	if err != nil {
		t.Fatal(err)
	}
	if receivedPrompt != "MEMORY_OVERRIDE" {
		t.Fatalf("prompt override was not used: %q", receivedPrompt)
	}
	seenCity, seenProject := false, false
	for _, fact := range facts {
		if fact.Namespace == "location" && fact.Predicate == "home_city" && fact.Value == "Москва" {
			seenCity = true
		}
		if fact.Namespace == "project" && fact.Subject == "jasmine" && fact.Predicate == "name" {
			seenProject = true
		}
	}
	if !seenCity || !seenProject {
		t.Fatalf("expected explicit city and extracted project, got %+v", facts)
	}
}
