package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

func (o *Orchestrator) correctMealObservation(ctx context.Context, tx pgx.Tx, e AssistantEvent, m MealMutation, target mealTarget) (*telegram.IngestionResponse, error) {
	var itemJSON json.RawMessage
	var oldAt time.Time
	var logicalID, mealType string
	err := tx.QueryRow(ctx, `SELECT item,eaten_at,logical_turn_id,meal_type FROM meal_observations WHERE id=$1 AND user_id=$2 AND persisted_entry_id IS NULL FOR UPDATE`, target.ObservationID, e.UserID).Scan(&itemJSON, &oldAt, &logicalID, &mealType)
	if errors.Is(err, pgx.ErrNoRows) {
		return mealClarification("meal_observation_not_found"), nil
	}
	if err != nil {
		return nil, err
	}
	var item FoodAnalysisItem
	if err := json.Unmarshal(itemJSON, &item); err != nil {
		return nil, err
	}
	if m.Operation == "refine_portion" && m.WeightG == nil && m.Calories == nil {
		estimate, err := o.estimateMealPortion(ctx, item, e.Text, m.VolumeML)
		if err != nil {
			return mealClarification("portion_estimate_unavailable"), nil
		}
		m.WeightG = estimate.WeightG
		m.Calories = estimate.Calories
		m.ProteinG = estimate.ProteinG
		m.FatG = estimate.FatG
		m.CarbsG = estimate.CarbsG
	}
	before := map[string]any{"item": item, "eaten_at": oldAt, "meal_type": mealType}
	at := oldAt
	switch m.Operation {
	case "correct_item_identity":
		if !strings.Contains(strings.ToLower(e.Text), strings.ToLower(m.NewName)) {
			return mealClarification("new_food_name_not_in_user_message"), nil
		}
		item.NameRU = m.NewName
	case "correct_date":
		timezone, err := o.mealUserTimezone(ctx, e.UserID)
		if err != nil {
			return nil, err
		}
		next, explicit, err := resolveImageTimeAt(time.Now().In(ResolveTimezone(timezone, "UTC")), m.DateText)
		if err != nil {
			return nil, err
		}
		if !explicit {
			return mealClarification("date_not_explicit"), nil
		}
		at = next
	case "correct_item_quantity", "refine_portion":
		if m.WeightG == nil && m.Calories == nil {
			return mealClarification("portion_estimate_needed"), nil
		}
		factor := 1.0
		if m.WeightG != nil {
			factor = *m.WeightG / item.WeightG
			item.WeightG = *m.WeightG
		}
		item.Calories *= factor
		item.ProteinG *= factor
		item.FatG *= factor
		item.CarbsG *= factor
		if m.Calories != nil {
			item.Calories = *m.Calories
		}
		if m.ProteinG != nil {
			item.ProteinG = *m.ProteinG
		}
		if m.FatG != nil {
			item.FatG = *m.FatG
		}
		if m.CarbsG != nil {
			item.CarbsG = *m.CarbsG
		}
	case "correct_nutrition":
		item.Calories = *m.Calories
		if m.ProteinG != nil {
			item.ProteinG = *m.ProteinG
		}
		if m.FatG != nil {
			item.FatG = *m.FatG
		}
		if m.CarbsG != nil {
			item.CarbsG = *m.CarbsG
		}
	case "remove_item":
		_, err := tx.Exec(ctx, `UPDATE meal_observations SET superseded_source_update_id=$2 WHERE id=$1`, target.ObservationID, e.UpdateID)
		if err != nil {
			return nil, err
		}
	default:
		return mealClarification("operation_requires_saved_meal"), nil
	}
	if item.Calories > item.WeightG*10 || item.ProteinG > item.WeightG*1.2 || item.FatG > item.WeightG*1.2 || item.CarbsG > item.WeightG*1.2 {
		return mealClarification("nutrition_implausible"), nil
	}
	updated, _ := json.Marshal(item)
	if m.Operation != "remove_item" {
		if _, err := tx.Exec(ctx, `UPDATE meal_observations SET item=$2::jsonb,eaten_at=$3 WHERE id=$1`, target.ObservationID, string(updated), at); err != nil {
			return nil, err
		}
	}
	after := map[string]any{"item": item, "eaten_at": at, "meal_type": mealType, "removed": m.Operation == "remove_item"}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	_, err = tx.Exec(ctx, `INSERT INTO meal_observation_corrections(user_id,observation_id,operation,before_value,after_value,source_update_id,source_message_id,correlation_id)
      VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8) ON CONFLICT(user_id,observation_id,source_update_id) DO NOTHING`, e.UserID, target.ObservationID, m.Operation, string(beforeJSON), string(afterJSON), e.UpdateID, e.MessageID, correlation.GetCorrelationID(ctx))
	if err != nil {
		return nil, err
	}
	if m.Operation == "correct_date" {
		_, err = tx.Exec(ctx, `UPDATE meal_observations SET eaten_at=$3 WHERE user_id=$1 AND logical_turn_id=$2 AND persisted_entry_id IS NULL`, e.UserID, logicalID, at)
		if err != nil {
			return nil, err
		}
	}
	if err := o.setMealContextTx(ctx, tx, e.UserID, "", mealType, at, e.UpdateID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE meal_conversation_context SET logical_turn_id=$2 WHERE user_id=$1`, e.UserID, logicalID); err != nil {
		return nil, err
	}
	facts := &telegram.ReplyContext{EventType: "meal_observation_corrected", ActionsPerformed: []string{"meal." + m.Operation}, CurrentState: map[string]any{"observation_id": target.ObservationID, "operation": m.Operation, "after": after, "persisted": false}}
	res, err := o.deliverStandAloneResponseTx(ctx, tx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, "", nil, facts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
func (o *Orchestrator) bulkSaveObservedMeals(ctx context.Context, e AssistantEvent, m MealMutation) (*telegram.IngestionResponse, error) {
	timezone, err := o.mealUserTimezone(ctx, e.UserID)
	if err != nil {
		return nil, err
	}
	text := m.DateText
	if text == "" {
		text = e.Text
	}
	at, explicit, err := resolveImageTimeAt(time.Now().In(ResolveTimezone(timezone, "UTC")), text)
	if err != nil {
		return nil, err
	}
	if !explicit {
		return mealClarification("bulk_meal_date_required"), nil
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,logical_turn_id,source_update_id,source_message_id,source_file_unique_id,image_index,item_index,item,meal_type,eaten_at FROM meal_observations
      WHERE user_id=$1 AND DATE(eaten_at AT TIME ZONE $2::text)=$3::date AND persisted_entry_id IS NULL AND superseded_source_update_id IS NULL ORDER BY created_at,image_index,item_index FOR UPDATE`, e.UserID, timezone, at.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	type candidate struct {
		ID, LogicalID         string
		UpdateID              *int64
		MessageID             int64
		FileID                *string
		ImageIndex, ItemIndex int
		Item                  json.RawMessage
		MealType              string
		EatenAt               time.Time
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.LogicalID, &c.UpdateID, &c.MessageID, &c.FileID, &c.ImageIndex, &c.ItemIndex, &c.Item, &c.MealType, &c.EatenAt); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	groups := map[string]string{}
	refs := []telegram.DomainReference{}
	saved := 0
	for _, c := range candidates {
		var item FoodAnalysisItem
		if err := json.Unmarshal(c.Item, &item); err != nil {
			return nil, err
		}
		if err := ValidateFoodDraft(&FoodAnalysisDraft{Items: []FoodAnalysisItem{item}}); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%d", c.LogicalID, c.ImageIndex)
		group := groups[key]
		if group == "" {
			if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&group); err != nil {
				return nil, err
			}
			groups[key] = group
		}
		name := item.NameRU
		if name == "" {
			name = item.NameEN
		}
		var entryID string
		err = tx.QueryRow(ctx, `INSERT INTO meal_entries(user_id,observed_name,meal_type,weight_g,calories,proteins,fats,carbs,eaten_at,meal_group_id,source_update_id,source_message_id,source_item_index,correlation_id,source_file_unique_id)
          VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
          ON CONFLICT(user_id,source_update_id,source_item_index) WHERE source_update_id IS NOT NULL DO UPDATE SET observed_name=meal_entries.observed_name RETURNING id`, e.UserID, name, c.MealType, item.WeightG, item.Calories, item.ProteinG, item.FatG, item.CarbsG, c.EatenAt, group, c.UpdateID, c.MessageID, c.ItemIndex, correlation.GetCorrelationID(ctx), c.FileID).Scan(&entryID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE meal_observations SET persisted_entry_id=$2 WHERE id=$1`, c.ID, entryID); err != nil {
			return nil, err
		}
		refs = append(refs, telegram.DomainReference{Type: "meal", ID: group, Extra: map[string]any{"entry_id": entryID, "name": name, "calories": item.Calories, "eaten_at": c.EatenAt}})
		saved++
	}
	event := "bulk_meals_no_new_items"
	actions := []string{}
	if saved > 0 {
		event = "bulk_meals_logged"
		actions = []string{"meals.bulk_save"}
	}
	facts := &telegram.ReplyContext{EventType: event, ActionsPerformed: actions, CurrentState: map[string]any{"date": at.Format("2006-01-02"), "saved_items": saved, "already_saved_or_unavailable": saved == 0}, ToolResults: refs}
	res, err := o.deliverStandAloneResponseTx(ctx, tx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, "", refs, facts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
