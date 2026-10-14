package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/assistant"
	"jasmine/internal/module/telegram"
)

func TestPostgreSQL_ProfileDocumentsExtractAllSectionsAndProjects(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username) VALUES('profile-document-case') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_users(telegram_user_id,user_id) VALUES(712345,$1)`, userID); err != nil {
		t.Fatal(err)
	}
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		chunk := ""
		for _, m := range request.Messages {
			if m.Role == "user" {
				chunk = m.Content
			}
		}
		facts := []map[string]any{}
		add := func(ns, subject, predicate string, value any, evidence string) {
			if strings.Contains(chunk, evidence) {
				facts = append(facts, map[string]any{"namespace": ns, "subject": subject, "predicate": predicate, "value": value, "operation": "replace", "confidence": 0.99, "evidence": evidence})
			}
		}
		add("project", "amazed_by_you", "name", "Jasmine", "Jasmine")
		add("project", "echo", "name", "Echo", "Echo")
		add("device", "laptop", "operating_system", "Arch Linux", "Arch Linux")
		add("assistant_preferences", "self", "final_sentence_period", false, "Не ставь точку в конце ответа")
		b, _ := json.Marshal(map[string]any{"facts": facts})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(b)}}}})
	}))
	defer ai.Close()
	client := openrouter.NewClientWithBaseURL("test-key", "main", ai.URL, ai.Client())
	repo := telegram.NewRepository(pool)
	orch := assistant.NewOrchestrator(pool, client, "main", "reply", nil, nil, nil, nil, nil, nil, nil, repo, nil)
	markdown := "# Имя\n**Эдуард**\n\n# Устройства\nНа ноуте Arch Linux\n\n# Предпочтения\nНе ставь точку в конце ответа\n\n" + strings.Repeat("## Заметки\nТестовая строка без личных фактов.\n\n", 900) + "# Проекты\nДелаю Jasmine - персонального ассистента\nТакже делаю Echo\n"
	send := func(updateID int64, name, mime, raw string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,raw_payload) VALUES($1,712345,712345,'{}'::jsonb)`, updateID); err != nil {
			t.Fatal(err)
		}
		doc := telegram.DocumentInput{Kind: telegram.ClassifyDocument(name, mime), Filename: name, MIMEType: mime, FileID: fmt.Sprintf("file-%d", updateID), MessageID: updateID, Text: raw}
		res, err := orch.IngestDocument(ctx, userID, 712345, updateID, updateID, nil, doc)
		if err != nil {
			t.Fatal(err)
		}
		if res == nil || !res.Success {
			t.Fatalf("import did not persist: %+v", res)
		}
	}
	send(910001, "profile.md", "text/markdown", markdown)
	facts, err := assistant.NewPersonalKnowledge(pool, nil, "").Relevant(ctx, userID, "какие у меня проекты")
	if err != nil {
		t.Fatal(err)
	}
	subjects := map[string]bool{}
	for _, f := range facts {
		subjects[f.Subject] = true
		if f.SourceType != "profile_document" || f.SourceFilename != "profile.md" || f.SourceDocumentID == nil {
			t.Fatalf("missing profile provenance: %+v", f)
		}
	}
	if !subjects["amazed_by_you"] || !subjects["echo"] {
		t.Fatalf("projects near document end missing: %+v", subjects)
	}
	jsonProfile := `{"person":{"name":"Эдуард"},"projects":[{"name":"Echo"}],"assistant_preferences":{"final_sentence_period":false}}`
	send(910002, "profile.json", "application/json", jsonProfile)
	send(910003, "profile.json", "application/json", jsonProfile)
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_memories WHERE user_id=$1 AND namespace='project' AND subject='echo' AND predicate='name' AND valid_until IS NULL`, userID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("duplicate active Echo facts: %d", active)
	}
	var docs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM personal_documents WHERE user_id=$1`, userID).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if docs != 3 {
		t.Fatalf("document provenance missing: %d", docs)
	}
}
