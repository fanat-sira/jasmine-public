package assistant

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
	"github.com/jackc/pgx/v5"
)

func isDailyMealsQuestion(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if !strings.Contains(t, "сегодня") && !strings.Contains(t, "за день") {
		return false
	}
	return strings.Contains(t, "что я съел") || strings.Contains(t, "что съел") || strings.Contains(t, "что ел") || strings.Contains(t, "что по еде") || strings.Contains(t, "сколько калорий") || strings.Contains(t, "итого") || strings.Contains(t, "кбжу")
}

func mealTypeFromText(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "завтрак"):
		return "breakfast"
	case strings.Contains(t, "обед"):
		return "lunch"
	case strings.Contains(t, "ужин"):
		return "dinner"
	case strings.Contains(t, "перекус"):
		return "snack"
	}
	return ""
}

var calorieNumber = regexp.MustCompile(`\b\d{2,4}\b`)

type mealCalorieCorrection struct {
	MealType string
	Calories int
}

func parseMealCorrection(text string) (mealCalorieCorrection, bool) {
	mealType := mealTypeFromText(text)
	if mealType == "" {
		return mealCalorieCorrection{}, false
	}
	t := strings.ToLower(text)
	if !strings.Contains(t, "исправ") && !strings.Contains(t, "постав") && !strings.Contains(t, "зафиксир") && !strings.Contains(t, "пересч") && !strings.Contains(t, "считай") && !strings.Contains(t, "берем") && !strings.Contains(t, "берём") && !strings.Contains(t, "запиши") {
		return mealCalorieCorrection{}, false
	}
	var selected int
	for _, token := range calorieNumber.FindAllString(t, -1) {
		value, _ := strconv.Atoi(token)
		if value >= 50 && value <= 5000 {
			selected = value
		}
	}
	if selected == 0 {
		return mealCalorieCorrection{}, false
	}
	return mealCalorieCorrection{MealType: mealType, Calories: selected}, true
}

func isMealCorrectionAffirmation(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "го", "да", "ну да", "давай", "ага", "угу", "ок", "окей":
		return true
	}
	return false
}

func proposedMealCorrection(prior string) (mealCalorieCorrection, bool) {
	prior = strings.ToLower(prior)
	start := -1
	for _, verb := range []string{"зафиксировать", "пересчитать", "поставить"} {
		if idx := strings.LastIndex(prior, verb); idx > start {
			start = idx
		}
	}
	if start < 0 {
		return mealCalorieCorrection{}, false
	}
	return parseMealCorrection(prior[start:])
}

func (o *Orchestrator) mealCorrectionRequest(ctx context.Context, chatID int64, text string) (mealCalorieCorrection, bool, error) {
	if correction, ok := parseMealCorrection(text); ok {
		return correction, true, nil
	}
	if !isMealCorrectionAffirmation(text) || o.telegramRepo == nil {
		return mealCalorieCorrection{}, false, nil
	}
	messages, err := o.telegramRepo.ListMessages(ctx, chatID, 8)
	if err != nil {
		return mealCalorieCorrection{}, false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Direction != telegram.DirectionOutbound || messages[i].Text == nil {
			continue
		}
		correction, ok := proposedMealCorrection(*messages[i].Text)
		return correction, ok, nil
	}
	return mealCalorieCorrection{}, false, nil
}

func (o *Orchestrator) answerDailyMeals(ctx context.Context, userID string, chatID, updateID, messageID int64) (*telegram.IngestionResponse, error) {
	var timezone string
	var target *int
	if err := o.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(NULLIF(u.timezone,'UTC'),''),NULLIF(tu.timezone,''),'UTC'),u.target_calories FROM users u LEFT JOIN telegram_users tu ON tu.user_id=u.id WHERE u.id=$1`, userID).Scan(&timezone, &target); err != nil {
		return nil, err
	}
	day := time.Now().In(ResolveTimezone(timezone, "UTC")).Format("2006-01-02")
	rows, err := o.db.Query(ctx, `SELECT meal_type, COALESCE(SUM(calories),0), COALESCE(SUM(proteins),0), COALESCE(SUM(fats),0), COALESCE(SUM(carbs),0) FROM meal_entries WHERE user_id=$1 AND deleted_at IS NULL AND DATE(eaten_at AT TIME ZONE $2::text)=$3::date GROUP BY meal_type ORDER BY CASE meal_type WHEN 'breakfast' THEN 1 WHEN 'lunch' THEN 2 WHEN 'dinner' THEN 3 ELSE 4 END`, userID, timezone, day)
	if err != nil {
		return nil, err
	}
	var mealRows []map[string]any
	var calories, protein, fat, carbs float64
	for rows.Next() {
		var mealType string
		var c, p, f, cb float64
		if err := rows.Scan(&mealType, &c, &p, &f, &cb); err != nil {
			rows.Close()
			return nil, err
		}
		mealRows = append(mealRows, map[string]any{"meal_type": mealType, "calories": c, "protein_g": p, "fat_g": f, "carbs_g": cb})
		calories += c
		protein += p
		fat += f
		carbs += cb
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if o.telegramRepo != nil {
		state, err := o.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && err != telegram.ErrNotFound {
			return nil, err
		}
		if state != nil && state.PendingClarification != nil && state.PendingClarification.Type == "image_unknown_needs_intent" {
			state.PendingClarification = nil
			if err := o.telegramRepo.UpsertConversationStateTx(ctx, tx, state); err != nil {
				return nil, err
			}
		}
	}
	res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, &telegram.ReplyContext{EventType: "daily_meal_summary", CurrentState: map[string]any{"date": day, "timezone": timezone, "meals": mealRows, "total_calories": calories, "protein_g": protein, "fat_g": fat, "carbs_g": carbs, "target_calories": target}})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func (o *Orchestrator) correctMealCalories(ctx context.Context, userID string, chatID, updateID, messageID int64, correction mealCalorieCorrection) (*telegram.IngestionResponse, error) {
	var timezone string
	if err := o.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(NULLIF(u.timezone,'UTC'),''),NULLIF(tu.timezone,''),'UTC') FROM users u LEFT JOIN telegram_users tu ON tu.user_id=u.id WHERE u.id=$1`, userID).Scan(&timezone); err != nil {
		return nil, err
	}
	day := time.Now().In(ResolveTimezone(timezone, "UTC")).Format("2006-01-02")
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var groupID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(meal_group_id,id) FROM meal_entries WHERE user_id=$1 AND deleted_at IS NULL AND meal_type=$2 AND DATE(eaten_at AT TIME ZONE $3::text)=$4::date ORDER BY eaten_at DESC,created_at DESC LIMIT 1`, userID, correction.MealType, timezone, day).Scan(&groupID)
	if err == pgx.ErrNoRows {
		res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, &telegram.ReplyContext{EventType: "meal_correction_not_found", Clarification: []string{"meal_not_found_for_today"}, CurrentState: map[string]any{"meal_type": correction.MealType, "date": day}})
		if err != nil {
			return nil, err
		}
		return res, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	var previous float64
	locked, err := tx.Query(ctx, `SELECT calories FROM meal_entries WHERE user_id=$1 AND deleted_at IS NULL AND COALESCE(meal_group_id,id)=$2 FOR UPDATE`, userID, groupID)
	if err != nil {
		return nil, err
	}
	for locked.Next() {
		var calories float64
		if err := locked.Scan(&calories); err != nil {
			locked.Close()
			return nil, err
		}
		previous += calories
	}
	if err := locked.Err(); err != nil {
		locked.Close()
		return nil, err
	}
	locked.Close()
	if previous <= 0 {
		return nil, fmt.Errorf("cannot correct meal with zero calories")
	}
	_, err = tx.Exec(ctx, `UPDATE meal_entries SET calories=calories*$3/$4 WHERE user_id=$1 AND COALESCE(meal_group_id,id)=$2`, userID, groupID, correction.Calories, previous)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO meal_calorie_corrections(user_id,meal_group_id,previous_calories,new_calories,source_update_id) VALUES ($1,$2,$3,$4,$5)`, userID, groupID, previous, correction.Calories, updateID)
	if err != nil {
		return nil, err
	}
	res, err := o.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, &telegram.ReplyContext{EventType: "meal_calories_corrected", ActionsPerformed: []string{"meal.calories_corrected"}, CurrentState: map[string]any{"meal_type": correction.MealType, "previous_calories": previous, "new_calories": correction.Calories, "macros_changed": false}})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
