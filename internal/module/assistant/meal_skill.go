package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type MealSkill struct{ o *Orchestrator }

func NewMealSkill(o *Orchestrator) *MealSkill                { return &MealSkill{o: o} }
func (s *MealSkill) Name() string                            { return "meal" }
func (s *MealSkill) ContextProvider() JasmineContextProvider { return nil }

const mealMutationSchema = `{"type":"object","properties":{"old_name":{"type":"string"},"new_name":{"type":"string"},"weight_g":{"type":"number"},"calories":{"type":"number"},"protein_g":{"type":"number"},"fat_g":{"type":"number"},"carbs_g":{"type":"number"},"volume_ml":{"type":"number"},"date_text":{"type":"string"},"meal_type":{"type":"string"}},"additionalProperties":false}`

func (s *MealSkill) Tools() []JasmineTool {
	definitions := []struct{ name, operation, description string }{
		{"bulk_save", "bulk_save", "Persist all unsaved food observations for an explicit local date; skip already saved entries"},
		{"correct_item_identity", "correct_item_identity", "Replace a visually inferred food identity with the user's explicit correction"},
		{"correct_item_quantity", "correct_item_quantity", "Change consumed grams and deterministically scale nutrition"},
		{"refine_portion", "refine_portion", "Use later portion context to recalculate and persist the current meal estimate"},
		{"correct_nutrition", "correct_nutrition", "Set corrected calories and optionally macros of the resolved meal item"},
		{"correct_date", "correct_date", "Move the resolved meal to an explicitly stated local date"},
		{"add_item", "add_item", "Add a known item with weight and nutrition to the resolved meal"},
		{"remove_item", "remove_item", "Mark a resolved item removed while retaining audit history"},
	}
	tools := []JasmineTool{{Spec: JasmineToolSpec{Name: "meal.read_current", Skill: "meal", Description: "Read active saved meal or pending observations and their date before a correction", RiskLevel: RiskRead}, Handle: func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		data, err := s.o.ReadCurrentMeal(ctx, e)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(data)
		return &JasmineToolResult{Facts: "current_meal_read", Data: encoded, OwnerUserID: e.UserID}, nil
	}}}
	for _, definition := range definitions {
		operation := definition.operation
		tools = append(tools, JasmineTool{Spec: JasmineToolSpec{Name: "meal." + definition.name, Skill: "meal", Description: definition.description, RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update", ArgumentSchema: json.RawMessage(mealMutationSchema)}, Handle: func(ctx context.Context, e AssistantEvent, _ *JasmineContext, input json.RawMessage) (*JasmineToolResult, error) {
			var mutation MealMutation
			if err := json.Unmarshal(input, &mutation); err != nil {
				return nil, err
			}
			mutation.Operation = operation
			res, err := s.o.ApplyMealMutation(ctx, e, mutation)
			if err != nil {
				return nil, err
			}
			return &JasmineToolResult{Response: res, Done: true, OwnerUserID: e.UserID}, nil
		}})
	}
	return tools
}
func (o *Orchestrator) ReadCurrentMeal(ctx context.Context, e AssistantEvent) (map[string]any, error) {
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	target, err := o.resolveMealTarget(ctx, tx, e, MealMutation{})
	if err != nil {
		if strings.Contains(err.Error(), "meal_target") {
			return map[string]any{"found": false, "reason": err.Error()}, nil
		}
		return nil, err
	}
	state := map[string]any{"found": true, "date": target.Date, "meal_type": target.MealType}
	if target.ObservationID != "" {
		var item json.RawMessage
		err := tx.QueryRow(ctx, `SELECT item FROM meal_observations WHERE id=$1 AND user_id=$2`, target.ObservationID, e.UserID).Scan(&item)
		if err != nil {
			return nil, err
		}
		state["observation_id"] = target.ObservationID
		state["item"] = item
		state["persisted"] = false
	} else if target.GroupID != "" {
		rows, err := tx.Query(ctx, `SELECT id,COALESCE(observed_name,''),weight_g,calories,proteins,fats,carbs,eaten_at FROM meal_entries WHERE user_id=$1 AND meal_group_id=$2 AND deleted_at IS NULL ORDER BY created_at`, e.UserID, target.GroupID)
		if err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for rows.Next() {
			var id, name string
			var w, c, p, f, cb float64
			var at any
			if err := rows.Scan(&id, &name, &w, &c, &p, &f, &cb, &at); err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, map[string]any{"id": id, "name": name, "weight_g": w, "calories": c, "protein_g": p, "fat_g": f, "carbs_g": cb, "eaten_at": at})
		}
		rows.Close()
		state["meal_group_id"] = target.GroupID
		state["items"] = items
		state["persisted"] = true
	}
	return state, nil
}

var containerVolumeRE = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*[-–]\s*(\d+(?:[.,]\d+)?)`)

func fallbackMealAction(text string) (*JasmineToolCall, bool) {
	lower := strings.ToLower(strings.TrimSpace(text))
	if (strings.Contains(lower, "запиши") || strings.Contains(lower, "сохрани")) && (strings.Contains(lower, "вчера") || strings.Contains(lower, "позавчера")) && (strings.Contains(lower, "все") || strings.Contains(lower, "всё")) {
		b, _ := json.Marshal(map[string]any{"date_text": text})
		return &JasmineToolCall{Name: "meal.bulk_save", Input: b}, true
	}
	if strings.HasPrefix(lower, "это именно ") || strings.HasPrefix(lower, "это свиные") {
		name := strings.TrimSpace(strings.Split(strings.TrimPrefix(strings.TrimPrefix(lower, "это именно "), "это "), "\n")[0])
		if name != "" {
			b, _ := json.Marshal(map[string]any{"new_name": name})
			return &JasmineToolCall{Name: "meal.correct_item_identity", Input: b}, true
		}
	}
	if strings.Contains(lower, "контейнер") {
		if match := containerVolumeRE.FindStringSubmatch(lower); len(match) == 3 {
			a, _ := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
			b, _ := strconv.ParseFloat(strings.ReplaceAll(match[2], ",", "."), 64)
			if a > 0 && b >= a && b <= 2 {
				payload, _ := json.Marshal(map[string]any{"volume_ml": (a + b) * 500})
				return &JasmineToolCall{Name: "meal.refine_portion", Input: payload}, true
			}
		}
	}
	if strings.Contains(lower, "это вчера было") {
		payload, _ := json.Marshal(map[string]any{"date_text": "вчера"})
		return &JasmineToolCall{Name: "meal.correct_date", Input: payload}, true
	}
	return nil, false
}
