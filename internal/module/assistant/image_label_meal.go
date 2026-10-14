package assistant

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

var labelCaptionGrams = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(?:г|гр|грамм)(?:\s|$|[.,;!?])`)

func wantsLogLabelMeal(caption string) bool {
	t := strings.ToLower(strings.TrimSpace(caption))
	if wantsSaveProduct(t) || (strings.Contains(t, "?") && !strings.Contains(t, "запиши")) || strings.Contains(t, "не съел") || strings.Contains(t, "не съела") || strings.Contains(t, "не поел") || strings.Contains(t, "не поела") {
		return false
	}
	for _, word := range []string{"завтрак", "обед", "ужин", "перекус", "breakfast", "lunch", "dinner", "snack", "съел", "съела", "поел", "поела"} {
		if strings.Contains(t, word) {
			return true
		}
	}
	return strings.Contains(t, "запиши") || strings.Contains(t, "добавь в дневник")
}

func labelMealWeight(d *NutritionLabelDraft, caption string) (float64, bool) {
	if m := labelCaptionGrams.FindStringSubmatch(caption); len(m) == 2 {
		if weight, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64); err == nil && weight > 0 && weight <= 5000 {
			return weight, true
		}
	}
	if d == nil {
		return 0, false
	}
	switch d.ValuesBasis {
	case "per_package":
		if d.PackageWeightG != nil && *d.PackageWeightG > 0 && *d.PackageWeightG <= 5000 {
			return *d.PackageWeightG, true
		}
	case "per_serving":
		if d.ServingSizeG != nil && *d.ServingSizeG > 0 && *d.ServingSizeG <= 5000 {
			return *d.ServingSizeG, true
		}
	case "per_100g":
		if d.PackageWeightG != nil && *d.PackageWeightG > 0 && *d.PackageWeightG <= 5000 {
			return *d.PackageWeightG, true
		}
	}
	return 0, false
}

func mealTypeTitle(mealType string) string {
	switch mealType {
	case "breakfast":
		return "завтрак"
	case "lunch":
		return "обед"
	case "dinner":
		return "ужин"
	default:
		return "перекус"
	}
}

func (o *Orchestrator) saveLabelMeal(ctx context.Context, userID string, chatID, updateID, messageID, sourceMessageID int64, caption string, image telegram.ImageInput, d *NutritionLabelDraft, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	weight, ok := labelMealWeight(d, caption)
	if !ok {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "meal_weight_needed", Clarification: []string{"consumed_weight_g"}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	per100, err := labelPer100(d)
	if err != nil {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "meal_nutrition_incomplete", Clarification: []string{"nutrition_values_or_basis"}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	at, mealType, err := o.mealPhotoTimeAndType(ctx, userID, caption)
	if err != nil {
		return nil, err
	}
	factor := weight / 100
	calories, protein, fat, carbs := per100.Calories*factor, per100.ProteinG*factor, per100.FatG*factor, per100.CarbsG*factor
	name := "Продукт с фото"
	if d.ProductName != nil && strings.TrimSpace(*d.ProductName) != "" {
		name = strings.TrimSpace(*d.ProductName)
	}
	return o.imageAction(ctx, userID, chatID, updateID, messageID, &refs, func(tx pgx.Tx) error {
		var groupID string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&groupID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO meal_entries (user_id,observed_name,meal_type,weight_g,calories,proteins,fats,carbs,eaten_at,meal_group_id,source_update_id,source_message_id,source_item_index,correlation_id,source_file_unique_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,$13,$14)`, userID, name, mealType, weight, calories, protein, fat, carbs, at, groupID, updateID, sourceMessageID, correlation.GetCorrelationID(ctx), image.FileUniqueID)
		if err != nil {
			return err
		}
		refs = append(refs, telegram.DomainReference{Type: "meal", ID: groupID, Extra: map[string]any{"meal_type": mealType, "name": name, "weight_g": weight, "calories": calories, "protein_g": protein, "fat_g": fat, "carbs_g": carbs, "eaten_at": at}})
		return o.setMealContextTx(ctx, tx, userID, groupID, mealType, at, updateID)
	})
}
