package assistant

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

func wantsSaveProduct(caption string) bool {
	t := strings.ToLower(caption)
	return (strings.Contains(t, "сохрани") || strings.Contains(t, "добавь") || strings.Contains(t, "запиши")) && (strings.Contains(t, "продукт") || strings.Contains(t, "этикет"))
}
func labelPer100(d *NutritionLabelDraft) (FoodAnalysisTotal, error) {
	if d == nil || d.Calories == nil || d.ProteinG == nil || d.FatG == nil || d.CarbsG == nil {
		return FoodAnalysisTotal{}, fmt.Errorf("incomplete label")
	}
	factor := 1.0
	switch d.ValuesBasis {
	case "per_100g":
	case "per_serving":
		if d.ServingSizeG == nil || *d.ServingSizeG <= 0 {
			return FoodAnalysisTotal{}, fmt.Errorf("serving size missing")
		}
		factor = 100 / *d.ServingSizeG
	case "per_package":
		if d.PackageWeightG == nil || *d.PackageWeightG <= 0 {
			return FoodAnalysisTotal{}, fmt.Errorf("package weight missing")
		}
		factor = 100 / *d.PackageWeightG
	default:
		return FoodAnalysisTotal{}, fmt.Errorf("nutrition basis unknown")
	}
	out := FoodAnalysisTotal{Calories: *d.Calories * factor, ProteinG: *d.ProteinG * factor, FatG: *d.FatG * factor, CarbsG: *d.CarbsG * factor}
	for _, v := range []float64{out.Calories, out.ProteinG, out.FatG, out.CarbsG} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1000 {
			return out, fmt.Errorf("nutrition per 100 g is implausible")
		}
	}
	if out.ProteinG > 120 || out.FatG > 120 || out.CarbsG > 120 {
		return out, fmt.Errorf("macros per 100 g are implausible")
	}
	return out, nil
}
func (o *Orchestrator) saveLabelProduct(ctx context.Context, userID string, chatID, updateID, messageID int64, image telegram.ImageInput, d *NutritionLabelDraft, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	if d.ProductName == nil || strings.TrimSpace(*d.ProductName) == "" || utf8.RuneCountInString(*d.ProductName) > 255 {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "product_name_needed", Clarification: []string{"product_name"}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	if d.Source == "estimated_from_ingredients" || d.Confidence < 0.7 {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "nutrition_label_low_confidence", Clarification: []string{"clearer_nutrition_label"}, CurrentState: map[string]any{"confidence": d.Confidence, "source": d.Source}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	values, err := labelPer100(d)
	if err != nil {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "nutrition_basis_needed", Clarification: []string{"nutrition_values_basis"}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	return o.imageAction(ctx, userID, chatID, updateID, messageID, &refs, func(tx pgx.Tx) error {
		var barcode *string
		if d.Barcode != nil && strings.TrimSpace(*d.Barcode) != "" {
			barcode = d.Barcode
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO products(name,calories,proteins,fats,carbs,source,owner_id,nutrition_source,source_barcode,source_values_basis,source_update_id,source_message_id,correlation_id,source_file_unique_id) VALUES ($1,$2,$3,$4,$5,'user',$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, strings.TrimSpace(*d.ProductName), values.Calories, values.ProteinG, values.FatG, values.CarbsG, userID, d.Source, barcode, d.ValuesBasis, updateID, messageID, correlation.GetCorrelationID(ctx), image.FileUniqueID).Scan(&id)
		if err != nil {
			return err
		}
		refs = append(refs, telegram.DomainReference{Type: "product", ID: id, Kind: d.Source, Extra: map[string]any{"name": *d.ProductName, "nutrition_per_100g": values}})
		return nil
	})
}
