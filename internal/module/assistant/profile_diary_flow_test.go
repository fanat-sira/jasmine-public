package assistant

import "testing"

func TestProfileJSONAndDiaryIntent(t *testing.T) {
	const profile = `{"person":{"sex":"male","height_cm":177.5},"measurements":[{"analysis_date":"19.01.2026","age":23,"weight_kg":71.5,"body_analysis":{"basal_metabolic_rate_kcal":1593}}]}`
	if raw, ok := profileJSONText(profile); !ok || raw == "" {
		t.Fatal("pasted profile JSON was not detected")
	}
	if _, _, err := validateProfileJSON(profile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateProfileJSON(`{"person":{"height_cm":999}}`); err == nil {
		t.Fatal("implausible profile height accepted")
	}
	if !isDailyMealsQuestion("что я съел сегодня") {
		t.Fatal("daily diary question must take precedence over image clarification")
	}
	if !isMealEstimateFollowup("Думаешь 570? Вот вид сбоку, отметка 0.6") {
		t.Fatal("side image must be treated as a follow-up")
	}
}

func TestMealCorrectionProposalParsing(t *testing.T) {
	c, ok := proposedMealCorrection("Завтрак 175 ккал. Обед 570 ккал. Итого 745. Пересчитать и зафиксировать обед на 800?")
	if !ok || c.MealType != "lunch" || c.Calories != 800 {
		t.Fatalf("wrong proposed correction: %+v %t", c, ok)
	}
	if _, ok := parseMealCorrection("Думаешь 570 ккал на обед?"); ok {
		t.Fatal("question must not silently update diary")
	}
}
