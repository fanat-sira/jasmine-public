package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"jasmine/internal/module/prompts"
	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"golang.org/x/sync/errgroup"
)

const maxProfileMarkdownBytes = 256 << 10

func profileMarkdownText(text string) (string, bool) {
	if !strings.HasPrefix(text, "/profile_md\n") {
		return "", false
	}
	return strings.TrimPrefix(text, "/profile_md\n"), true
}

var markdownAge = regexp.MustCompile(`(?i)(\d{1,3})\s*(?:год|года|лет)`)
var markdownHeight = regexp.MustCompile(`(?i)(\d{3}(?:[.,]\d+)?)\s*см`)

// Import only fields that can be identified from explicit profile sections.
// Other prose stays in the source document and is not treated as verified fact.
func markdownProfileFacts(raw string) []FactProposal {
	lines := strings.Split(raw, "\n")
	section := ""
	facts := map[string]FactProposal{}
	set := func(key string, value any, evidence string) {
		if evidence == "" {
			return
		}
		facts[key] = FactProposal{Namespace: "identity", Subject: "self", Predicate: key, Value: value, Operation: "replace", Confidence: 1, Evidence: strings.TrimSpace(evidence)}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "# ")))
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(section, "имя") && !strings.Contains(section, "фамили") && facts["name"].Predicate == "" && (strings.HasPrefix(trimmed, "**") || strings.HasPrefix(lower, "имя:")) {
			candidate := strings.Trim(trimmed, " *.-")
			if slash := strings.Index(candidate, " /"); slash >= 0 {
				candidate = candidate[:slash]
			}
			candidate = strings.TrimSpace(strings.Trim(candidate, "*."))
			if len([]rune(candidate)) >= 2 && len([]rune(candidate)) <= 80 && !strings.ContainsAny(candidate, ":@#") && !strings.HasPrefix(candidate, "-") {
				set("name", candidate, trimmed)
			}
		}
		if strings.Contains(section, "возраст") && facts["age"].Predicate == "" {
			if match := markdownAge.FindStringSubmatch(trimmed); len(match) == 2 {
				age, _ := strconv.Atoi(match[1])
				if age >= 1 && age <= 120 {
					set("age", age, trimmed)
				}
			}
		}
		if (strings.Contains(section, "рост") || strings.Contains(section, "телесные показатели")) && facts["height_cm"].Predicate == "" {
			if match := markdownHeight.FindStringSubmatch(trimmed); len(match) == 2 {
				height, _ := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
				if height >= 100 && height <= 250 {
					set("height_cm", height, trimmed)
				}
			}
		}
		if strings.Contains(section, "пол и возраст") && facts["sex"].Predicate == "" {
			if strings.Contains(lower, "мужчина") {
				set("sex", "male", trimmed)
			} else if strings.Contains(lower, "женщина") {
				set("sex", "female", trimmed)
			}
		}
	}
	result := make([]FactProposal, 0, len(facts))
	for _, key := range []string{"name", "age", "sex", "height_cm"} {
		if fact, ok := facts[key]; ok {
			result = append(result, fact)
		}
	}
	return result
}

const markdownExtractionChunkBytes = 24 << 10

func markdownChunks(text string) []string {
	chunks := []string{}
	for len(text) > 0 {
		end := len(text)
		if end > markdownExtractionChunkBytes {
			end = markdownExtractionChunkBytes
			if split := strings.LastIndex(text[:end], "\n\n"); split > markdownExtractionChunkBytes/2 {
				end = split + 2
			} else if split := strings.LastIndexByte(text[:end], '\n'); split > markdownExtractionChunkBytes/2 {
				end = split + 1
			}
			for end > 0 && !utf8.ValidString(text[:end]) {
				end--
			}
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks
}

// ExtractMarkdown is called only after a Telegram profile.md document is received.
func (k *PersonalKnowledge) ExtractMarkdown(ctx context.Context, raw string) ([]FactProposal, error) {
	if k == nil || k.client == nil || k.model == "" {
		return nil, fmt.Errorf("main model unavailable")
	}
	chunks := markdownChunks(raw)
	results := make([][]FactProposal, len(chunks))
	var group errgroup.Group
	group.SetLimit(4)
	var failed []error
	var failures sync.Mutex
	for i, chunk := range chunks {
		i, chunk := i, chunk
		group.Go(func() error {
			prompt, err := prompts.Resolve(ctx, k.prompts, prompts.KeyProfileDocument)
			if err != nil {
				failures.Lock()
				failed = append(failed, err)
				failures.Unlock()
				return nil
			}
			callCtx, cancel := context.WithTimeout(correlation.WithOperation(ctx, "profile_markdown_extraction"), 55*time.Second)
			defer cancel()
			response, err := k.client.SendConversation(callCtx, k.model, prompt, nil, chunk)
			if err != nil {
				failures.Lock()
				failed = append(failed, err)
				failures.Unlock()
				return nil
			}
			var decoded struct {
				Facts []FactProposal `json:"facts"`
			}
			if err := json.Unmarshal([]byte(stripMarkdownFences(response)), &decoded); err != nil {
				failures.Lock()
				failed = append(failed, fmt.Errorf("profile document extraction response: %w", err))
				failures.Unlock()
				return nil
			}
			for _, fact := range decoded.Facts {
				if fact.Operation != "replace" && fact.Operation != "merge" && fact.Operation != "append" {
					continue
				}
				if len(fact.Evidence) == 0 || len(fact.Evidence) > 500 || !strings.Contains(strings.ToLower(chunk), strings.ToLower(fact.Evidence)) {
					continue
				}
				encoded, _ := json.Marshal(fact.Value)
				if len(encoded) > 1024 {
					continue
				}
				if normalized, ok := normalizeProposal(fact); ok {
					results[i] = append(results[i], normalized)
				}
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	selected := map[string]FactProposal{}
	order := []string{}
	for _, batch := range results {
		for _, fact := range batch {
			key := fact.Namespace + "/" + fact.Subject + "/" + fact.Predicate
			if _, exists := selected[key]; !exists {
				order = append(order, key)
			}
			selected[key] = fact
		}
	}
	facts := make([]FactProposal, 0, len(order))
	for _, key := range order {
		facts = append(facts, selected[key])
	}
	return facts, errors.Join(failed...)
}

func (o *Orchestrator) saveProfileMarkdown(ctx context.Context, userID string, chatID, updateID, messageID int64, raw string, source ...*telegram.DocumentInput) (*telegram.IngestionResponse, error) {
	if o.db == nil || o.knowledge == nil {
		return nil, fmt.Errorf("profile persistence unavailable")
	}
	if !telegram.IsProfileMarkdownUpload(ctx) {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "profile_markdown_upload_required", Clarification: []string{"send_profile_md_as_telegram_document"}}}, nil
	}
	if strings.TrimSpace(raw) == "" || len(raw) > maxProfileMarkdownBytes || !utf8.ValidString(raw) {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "profile_markdown_invalid", Clarification: []string{"valid_utf8_markdown_up_to_256_kib_required"}}}, nil
	}
	modelFacts, extractionErr := o.knowledge.ExtractMarkdown(ctx, raw)
	selected := map[string]FactProposal{}
	order := []string{}
	for _, fact := range append(markdownProfileFacts(raw), modelFacts...) {
		key := fact.Namespace + "/" + fact.Subject + "/" + fact.Predicate
		if _, exists := selected[key]; !exists {
			order = append(order, key)
		}
		selected[key] = fact
	}
	facts := make([]FactProposal, 0, len(order))
	for _, key := range order {
		facts = append(facts, selected[key])
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET profile_markdown=$2,profile_markdown_updated_at=NOW(),profile_markdown_source_update_id=$3,profile_markdown_source_message_id=$4,updated_at=NOW() WHERE id=$1`, userID, raw, updateID, messageID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("profile owner not found")
	}
	documentID, err := savePersonalDocumentTx(ctx, tx, userID, updateID, messageID, raw, source...)
	if err != nil {
		return nil, err
	}
	imported := 0
	unchanged := 0
	projects := map[string]bool{}
	for _, fact := range facts {
		if fact.Namespace == "project" {
			projects[fact.Subject] = true
		}
		sourceType := "profile_import"
		if documentID != nil {
			sourceType = "profile_document"
		}
		operation, err := o.knowledge.applyTx(ctx, tx, userID, &updateID, &messageID, sourceType, fact, documentID)
		if err != nil {
			return nil, err
		}
		if operation != "" {
			imported++
		} else {
			unchanged++
		}
	}
	reply := &telegram.ReplyContext{EventType: "profile_markdown_saved", ActionsPerformed: []string{"profile.markdown_import"}, CurrentState: map[string]any{"source_file": func() string {
		if len(source) > 0 && source[0] != nil {
			return source[0].Filename
		}
		return "profile.md"
	}(), "source_document_id": documentID, "bytes": len(raw), "recognized_facts": len(facts), "facts_created_or_updated": imported, "facts_unchanged": unchanged, "projects_found": len(projects), "extraction_incomplete": extractionErr != nil}}
	result, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, reply)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
