package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
)

const maxProfileBytes = 64 << 10

func profileJSONText(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/profile" {
		return "", true
	}
	if strings.HasPrefix(trimmed, "/profile\n") || strings.HasPrefix(trimmed, "/profile ") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "/profile")), true
	}
	if strings.HasPrefix(trimmed, "{") && (strings.Contains(trimmed, `"person"`) || strings.Contains(trimmed, `"measurements"`) || strings.Contains(trimmed, `"target_calories"`) || strings.Contains(trimmed, `"training_preferences"`) || strings.Contains(trimmed, `"nutrition_preferences"`) || strings.Contains(trimmed, `"goal"`)) {
		return trimmed, true
	}
	return "", false
}

func validateProfileJSON(raw string) (json.RawMessage, map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxProfileBytes || !json.Valid([]byte(raw)) {
		return nil, nil, fmt.Errorf("invalid_or_oversized_profile_json")
	}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil || data == nil {
		return nil, nil, fmt.Errorf("profile_must_be_object")
	}
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("profile_fields_missing")
	}
	if personValue, exists := data["person"]; exists && personValue != nil {
		if _, ok := personValue.(map[string]any); !ok {
			return nil, nil, fmt.Errorf("person_must_be_object")
		}
	}
	if person, ok := data["person"].(map[string]any); ok {
		if height, ok := person["height_cm"].(json.Number); ok {
			v, e := height.Float64()
			if e != nil || v < 100 || v > 250 {
				return nil, nil, fmt.Errorf("height_cm_out_of_range_100_250")
			}
		}
	}
	if measurementsValue, exists := data["measurements"]; exists && measurementsValue != nil {
		measurements, ok := measurementsValue.([]any)
		if !ok || len(measurements) > 100 {
			return nil, nil, fmt.Errorf("measurements_invalid_or_too_many")
		}
	}
	if value, ok := data["timezone"].(string); ok && value != "" {
		if _, err := time.LoadLocation(value); err != nil {
			return nil, nil, fmt.Errorf("timezone_unknown")
		}
	}
	if goal, exists := data["goal"]; exists && goal != nil {
		if _, ok := goal.(string); !ok {
			return nil, nil, fmt.Errorf("goal_must_be_string")
		}
	}
	if target, exists := data["target_calories"]; exists && target != nil {
		number, ok := target.(json.Number)
		if !ok {
			return nil, nil, fmt.Errorf("target_calories_must_be_number")
		}
		v, e := number.Int64()
		if e != nil || v < 500 || v > 10000 {
			return nil, nil, fmt.Errorf("target_calories_out_of_range_500_10000")
		}
	}
	canonical, _ := json.Marshal(data)
	return canonical, data, nil
}

func (o *Orchestrator) saveProfileJSON(ctx context.Context, userID string, chatID, updateID, messageID int64, raw string, source ...*telegram.DocumentInput) (*telegram.IngestionResponse, error) {
	if o.db == nil {
		return nil, fmt.Errorf("profile persistence is not configured")
	}
	canonical, data, err := validateProfileJSON(raw)
	if err != nil {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "profile_invalid", Clarification: []string{err.Error()}, CurrentState: map[string]any{"max_bytes": maxProfileBytes}}}, nil
	}
	var goal any
	if value, ok := data["goal"].(string); ok && strings.TrimSpace(value) != "" {
		goal = strings.TrimSpace(value)
	}
	var target any
	if value, ok := data["target_calories"].(json.Number); ok {
		target, _ = value.Int64()
	}
	var timezone any
	if value, ok := data["timezone"].(string); ok && value != "" {
		timezone = value
	}
	var extracted []FactProposal
	var extractionErr error
	if len(source) > 0 && source[0] != nil && o.knowledge != nil {
		extracted, extractionErr = o.knowledge.ExtractMarkdown(ctx, raw)
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET profile_data=$2::jsonb,profile_updated_at=NOW(),goal=COALESCE($3,goal),target_calories=COALESCE($4,target_calories),timezone=COALESCE($5,timezone),updated_at=NOW() WHERE id=$1`, userID, string(canonical), goal, target, timezone)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("user profile owner not found")
	}
	documentID, err := savePersonalDocumentTx(ctx, tx, userID, updateID, messageID, raw, source...)
	if err != nil {
		return nil, err
	}
	if o.knowledge != nil {
		if err := o.knowledge.ImportProfileTx(ctx, tx, userID, &updateID, &messageID, data); err != nil {
			return nil, err
		}
	}

	imported := 0
	projects := map[string]bool{}
	if o.knowledge != nil && documentID != nil {
		for _, fact := range extracted {
			if fact.Namespace == "project" {
				projects[fact.Subject] = true
			}
			op, err := o.knowledge.applyTx(ctx, tx, userID, &updateID, &messageID, "profile_json", fact, documentID)
			if err != nil {
				return nil, err
			}
			if op != "" {
				imported++
			}
		}
	}

	fields := make([]string, 0, len(data))
	for key := range data {
		fields = append(fields, key)
	}
	res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, &telegram.ReplyContext{EventType: "profile_saved", ActionsPerformed: []string{"profile.save"}, CurrentState: map[string]any{"goal": goal, "target_calories": target, "timezone": timezone, "profile_fields": fields, "source_document_id": documentID, "facts_created_or_updated": imported, "projects_found": len(projects), "extraction_incomplete": extractionErr != nil}})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
