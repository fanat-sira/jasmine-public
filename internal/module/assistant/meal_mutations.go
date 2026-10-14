package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

type MealMutation struct {
	Operation string   `json:"operation"`
	OldName   string   `json:"old_name,omitempty"`
	NewName   string   `json:"new_name,omitempty"`
	WeightG   *float64 `json:"weight_g,omitempty"`
	Calories  *float64 `json:"calories,omitempty"`
	ProteinG  *float64 `json:"protein_g,omitempty"`
	FatG      *float64 `json:"fat_g,omitempty"`
	CarbsG    *float64 `json:"carbs_g,omitempty"`
	VolumeML  *float64 `json:"volume_ml,omitempty"`
	DateText  string   `json:"date_text,omitempty"`
	MealType  string   `json:"meal_type,omitempty"`
}
type mealTarget struct {
	GroupID       string
	ObservationID string
	Date          time.Time
	MealType      string
}
type mealEntryRow struct {
	ID, Name, MealType                        string
	WeightG, Calories, ProteinG, FatG, CarbsG float64
	EatenAt                                   time.Time
	DeletedAt                                 *time.Time
}

func (r mealEntryRow) snapshot() map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name, "meal_type": r.MealType, "weight_g": r.WeightG, "calories": r.Calories, "protein_g": r.ProteinG, "fat_g": r.FatG, "carbs_g": r.CarbsG, "eaten_at": r.EatenAt, "deleted": r.DeletedAt != nil}
}
func validateMealMutation(m MealMutation) error {
	switch m.Operation {
	case "correct_date", "correct_item_identity", "correct_item_quantity", "correct_nutrition", "refine_portion", "add_item", "remove_item", "bulk_save":
	default:
		return fmt.Errorf("unsupported meal operation")
	}
	for _, v := range []*float64{m.WeightG, m.Calories, m.ProteinG, m.FatG, m.CarbsG, m.VolumeML} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 10000) {
			return fmt.Errorf("invalid meal number")
		}
	}
	if m.WeightG != nil && (*m.WeightG < 1 || *m.WeightG > 5000) {
		return fmt.Errorf("invalid meal weight")
	}
	if m.VolumeML != nil && (*m.VolumeML < 10 || *m.VolumeML > 5000) {
		return fmt.Errorf("invalid meal volume")
	}
	if m.Operation == "correct_item_identity" && strings.TrimSpace(m.NewName) == "" {
		return fmt.Errorf("food identity required")
	}
	if m.Operation == "correct_item_quantity" && m.WeightG == nil {
		return fmt.Errorf("weight required")
	}
	if m.Operation == "correct_nutrition" && m.Calories == nil {
		return fmt.Errorf("calories required")
	}
	if m.Operation == "add_item" && (m.NewName == "" || m.WeightG == nil || m.Calories == nil) {
		return fmt.Errorf("item name, weight and calories required")
	}
	if m.Operation == "correct_date" && m.DateText == "" {
		return fmt.Errorf("date required")
	}
	return nil
}
func (o *Orchestrator) mealUserTimezone(ctx context.Context, userID string) (string, error) {
	var timezone string
	err := o.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(NULLIF(u.timezone,'UTC'),''),NULLIF(tu.timezone,''),'UTC') FROM users u LEFT JOIN telegram_users tu ON tu.user_id=u.id WHERE u.id=$1`, userID).Scan(&timezone)
	return timezone, err
}
func (o *Orchestrator) resolveMealTarget(ctx context.Context, tx pgx.Tx, e AssistantEvent, m MealMutation) (mealTarget, error) {
	var target mealTarget
	timezone, err := o.mealUserTimezone(ctx, e.UserID)
	if err != nil {
		return target, err
	}
	loc := ResolveTimezone(timezone, "UTC")
	dateText := m.DateText
	if dateText == "" {
		dateText = e.Text
	}
	at, explicit, err := resolveImageTimeAt(time.Now().In(loc), dateText)
	if err != nil {
		return target, err
	}
	if m.Operation == "correct_date" {
		explicit = false
	}
	mealType := m.MealType
	if mealType == "" {
		mealType = mealTypeFromText(e.Text)
	}
	if explicit && mealType != "" {
		rows, err := tx.Query(ctx, `SELECT DISTINCT meal_group_id FROM meal_entries WHERE user_id=$1 AND meal_group_id IS NOT NULL AND deleted_at IS NULL AND meal_type=$2 AND DATE(eaten_at AT TIME ZONE $3::text)=$4::date`, e.UserID, mealType, timezone, at.Format("2006-01-02"))
		if err != nil {
			return target, err
		}
		groups := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return target, err
			}
			groups = append(groups, id)
		}
		rows.Close()
		if len(groups) == 1 {
			target.GroupID = groups[0]
			target.Date = at
			target.MealType = mealType
			return target, nil
		}
		if len(groups) > 1 {
			return target, fmt.Errorf("ambiguous_meal_target")
		}
	}
	if e.ReplyToMessageID != nil {
		rows, err := tx.Query(ctx, `SELECT DISTINCT meal_group_id FROM meal_entries WHERE user_id=$1 AND source_message_id=$2 AND meal_group_id IS NOT NULL AND deleted_at IS NULL`, e.UserID, *e.ReplyToMessageID)
		if err != nil {
			return target, err
		}
		groups := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return target, err
			}
			groups = append(groups, id)
		}
		rows.Close()
		if len(groups) == 1 {
			target.GroupID = groups[0]
			return target, nil
		}
		if len(groups) > 1 {
			return target, fmt.Errorf("ambiguous_meal_target")
		}
	}
	var groupID, logicalID *string
	var contextDate time.Time
	var contextType string
	err = tx.QueryRow(ctx, `SELECT meal_group_id,logical_turn_id,eaten_at,meal_type FROM meal_conversation_context WHERE user_id=$1 AND active_until>NOW()`, e.UserID).Scan(&groupID, &logicalID, &contextDate, &contextType)
	if err == nil {
		target.Date = contextDate
		target.MealType = contextType
		if explicit {
			target.Date = at
		}
		if groupID != nil && (!explicit || sameMealDay(contextDate, at, loc)) {
			target.GroupID = *groupID
			return target, nil
		}
		if logicalID != nil && (!explicit || sameMealDay(contextDate, at, loc)) {
			var persistedID *string
			err = tx.QueryRow(ctx, `SELECT id,persisted_entry_id FROM meal_observations WHERE user_id=$1 AND logical_turn_id=$2 AND meal_type=$3 AND superseded_source_update_id IS NULL ORDER BY image_index DESC,item_index DESC LIMIT 1`, e.UserID, *logicalID, contextType).Scan(&target.ObservationID, &persistedID)
			if err == nil {
				if persistedID == nil {
					return target, nil
				}
				if err := tx.QueryRow(ctx, `SELECT meal_group_id FROM meal_entries WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, *persistedID, e.UserID).Scan(&target.GroupID); err == nil {
					target.ObservationID = ""
					return target, nil
				} else if !errors.Is(err, pgx.ErrNoRows) {
					return target, err
				}
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return target, err
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return target, err
	}
	if explicit {
		rows, err := tx.Query(ctx, `SELECT DISTINCT meal_group_id FROM meal_entries WHERE user_id=$1 AND meal_group_id IS NOT NULL AND deleted_at IS NULL AND DATE(eaten_at AT TIME ZONE $2::text)=$3::date`, e.UserID, timezone, at.Format("2006-01-02"))
		if err != nil {
			return target, err
		}
		groups := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return target, err
			}
			groups = append(groups, id)
		}
		rows.Close()
		if len(groups) == 1 {
			target.GroupID = groups[0]
			target.Date = at
			return target, nil
		}
		if len(groups) > 1 {
			return target, fmt.Errorf("ambiguous_meal_target")
		}
	}
	err = tx.QueryRow(ctx, `SELECT meal_group_id FROM meal_entries WHERE user_id=$1 AND meal_group_id IS NOT NULL AND deleted_at IS NULL AND created_at>NOW()-INTERVAL '12 hours' ORDER BY created_at DESC LIMIT 1`, e.UserID).Scan(&target.GroupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, fmt.Errorf("meal_target_not_found")
	}
	return target, err
}
func sameMealDay(a, b time.Time, loc *time.Location) bool {
	return a.In(loc).Format("2006-01-02") == b.In(loc).Format("2006-01-02")
}
func mealClarification(code string) *telegram.IngestionResponse {
	return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "meal_correction_needs_clarification", Clarification: []string{code}}}
}
func (o *Orchestrator) ApplyMealMutation(ctx context.Context, e AssistantEvent, m MealMutation) (*telegram.IngestionResponse, error) {
	if err := validateMealMutation(m); err != nil {
		return mealClarification(err.Error()), nil
	}
	if e.UpdateID != 0 {
		var delivered bool
		if err := o.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_deliveries WHERE update_id=$1 AND user_id=$2)`, e.UpdateID, e.UserID).Scan(&delivered); err != nil {
			return nil, err
		}
		if delivered {
			return &telegram.IngestionResponse{Success: true, ReplyContext: &telegram.ReplyContext{EventType: "meal_mutation_already_applied"}}, nil
		}
	}
	if m.Operation == "bulk_save" {
		return o.bulkSaveObservedMeals(ctx, e, m)
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	target, err := o.resolveMealTarget(ctx, tx, e, m)
	if err != nil {
		if strings.Contains(err.Error(), "meal_target") {
			return mealClarification(err.Error()), nil
		}
		return nil, err
	}
	if target.ObservationID != "" {
		return o.correctMealObservation(ctx, tx, e, m, target)
	}
	if target.GroupID == "" {
		return mealClarification("meal_target_not_found"), nil
	}
	rows, err := tx.Query(ctx, `SELECT id,COALESCE(observed_name,''),meal_type,weight_g,calories,proteins,fats,carbs,eaten_at,deleted_at FROM meal_entries WHERE user_id=$1 AND meal_group_id=$2 AND deleted_at IS NULL ORDER BY created_at,id FOR UPDATE`, e.UserID, target.GroupID)
	if err != nil {
		return nil, err
	}
	entries := []mealEntryRow{}
	for rows.Next() {
		var item mealEntryRow
		if err := rows.Scan(&item.ID, &item.Name, &item.MealType, &item.WeightG, &item.Calories, &item.ProteinG, &item.FatG, &item.CarbsG, &item.EatenAt, &item.DeletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(entries) == 0 {
		return mealClarification("meal_target_not_found"), nil
	}
	affected := []mealEntryRow{}
	if m.Operation == "correct_date" {
		affected = entries
	} else if m.Operation != "add_item" {
		for _, item := range entries {
			if m.OldName == "" || strings.Contains(strings.ToLower(item.Name), strings.ToLower(m.OldName)) {
				affected = append(affected, item)
			}
		}
		if len(affected) != 1 {
			return mealClarification("meal_item_ambiguous"), nil
		}
	}
	if m.Operation == "refine_portion" && m.WeightG == nil && m.Calories == nil && len(affected) == 1 {
		estimate, err := o.estimateMealPortion(ctx, affected[0].snapshot(), e.Text, m.VolumeML)
		if err != nil {
			return mealClarification("portion_estimate_unavailable"), nil
		}
		m.WeightG = estimate.WeightG
		m.Calories = estimate.Calories
		m.ProteinG = estimate.ProteinG
		m.FatG = estimate.FatG
		m.CarbsG = estimate.CarbsG
	}
	updates := []map[string]any{}
	for _, item := range affected {
		before := item.snapshot()
		switch m.Operation {
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
			item.EatenAt = next
		case "correct_item_identity":
			if !strings.Contains(strings.ToLower(e.Text), strings.ToLower(m.NewName)) {
				return mealClarification("new_food_name_not_in_user_message"), nil
			}
			item.Name = strings.TrimSpace(m.NewName)
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
				energyFactor := 1.0
				if item.Calories > 0 {
					energyFactor = *m.Calories / item.Calories
				}
				item.Calories = *m.Calories
				if m.ProteinG == nil {
					item.ProteinG *= energyFactor
				}
				if m.FatG == nil {
					item.FatG *= energyFactor
				}
				if m.CarbsG == nil {
					item.CarbsG *= energyFactor
				}
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
			now := time.Now()
			item.DeletedAt = &now
		}
		if item.Calories > item.WeightG*10 || item.ProteinG > item.WeightG*1.2 || item.FatG > item.WeightG*1.2 || item.CarbsG > item.WeightG*1.2 {
			return mealClarification("nutrition_implausible"), nil
		}
		tag, err := tx.Exec(ctx, `UPDATE meal_entries SET observed_name=$2,weight_g=$3,calories=$4,proteins=$5,fats=$6,carbs=$7,eaten_at=$8,deleted_at=$9 WHERE id=$1 AND user_id=$10`, item.ID, item.Name, item.WeightG, item.Calories, item.ProteinG, item.FatG, item.CarbsG, item.EatenAt, item.DeletedAt, e.UserID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("meal update lost")
		}
		after := item.snapshot()
		updates = append(updates, after)
		if err := auditMealEntryTx(ctx, tx, e, item.ID, m.Operation, before, after); err != nil {
			return nil, err
		}
	}
	if m.Operation == "add_item" {
		base := entries[0]
		var newID string
		err = tx.QueryRow(ctx, `INSERT INTO meal_entries(user_id,observed_name,meal_type,weight_g,calories,proteins,fats,carbs,eaten_at,meal_group_id,source_update_id,source_message_id,source_item_index,correlation_id)
         VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`, e.UserID, m.NewName, base.MealType, *m.WeightG, *m.Calories, valueOrZero(m.ProteinG), valueOrZero(m.FatG), valueOrZero(m.CarbsG), base.EatenAt, target.GroupID, e.UpdateID, e.MessageID, len(entries), correlation.GetCorrelationID(ctx)).Scan(&newID)
		if err != nil {
			return nil, err
		}
		after := map[string]any{"id": newID, "name": m.NewName, "weight_g": *m.WeightG, "calories": *m.Calories}
		if err := auditMealEntryTx(ctx, tx, e, newID, m.Operation, map[string]any{}, after); err != nil {
			return nil, err
		}
		updates = append(updates, after)
	}
	var total float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(calories),0) FROM meal_entries WHERE user_id=$1 AND meal_group_id=$2 AND deleted_at IS NULL`, e.UserID, target.GroupID).Scan(&total); err != nil {
		return nil, err
	}
	facts := &telegram.ReplyContext{EventType: "meal_corrected", ActionsPerformed: []string{"meal." + m.Operation}, CurrentState: map[string]any{"meal_group_id": target.GroupID, "operation": m.Operation, "changed_items": updates, "meal_total_calories": total}, ToolResults: []telegram.DomainReference{{Type: "meal", ID: target.GroupID, Extra: map[string]any{"calories": total}}}}
	res, err := o.deliverStandAloneResponseTx(ctx, tx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, "", facts.ToolResults, facts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
func valueOrZero(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
func auditMealEntryTx(ctx context.Context, tx pgx.Tx, e AssistantEvent, entryID, operation string, before, after map[string]any) error {
	oldJSON, _ := json.Marshal(before)
	newJSON, _ := json.Marshal(after)
	_, err := tx.Exec(ctx, `INSERT INTO meal_entry_corrections(user_id,meal_entry_id,operation,before_value,after_value,source_update_id,source_message_id,correlation_id,source_text)
     VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9) ON CONFLICT(user_id,meal_entry_id,source_update_id) DO NOTHING`, e.UserID, entryID, operation, string(oldJSON), string(newJSON), e.UpdateID, e.MessageID, correlation.GetCorrelationID(ctx), e.Text)
	return err
}
