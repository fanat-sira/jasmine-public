package assistant

import "testing"

func TestNutritionLabelBreakfastPortion(t *testing.T) {
	d := &NutritionLabelDraft{
		ValuesBasis:    "per_package",
		PackageWeightG: ptr(50.0),
		Calories:       ptr(175.0),
		ProteinG:       ptr(7.0),
		FatG:           ptr(4.0),
		CarbsG:         ptr(28.0),
	}
	if !wantsLogLabelMeal("Завтрак") {
		t.Fatal("breakfast caption must trigger diary logging")
	}
	weight, ok := labelMealWeight(d, "Завтрак")
	if !ok || weight != 50 {
		t.Fatalf("wrong package weight: %.1f, %t", weight, ok)
	}
	per100, err := labelPer100(d)
	if err != nil || per100.Calories*weight/100 != 175 || per100.ProteinG*weight/100 != 7 {
		t.Fatalf("wrong meal values: %+v, %v", per100, err)
	}
	weight, ok = labelMealWeight(d, "Завтрак, 25 г и кофе")
	if !ok || weight != 25 || per100.Calories*weight/100 != 87.5 {
		t.Fatalf("explicit portion not applied: %.1f, %t", weight, ok)
	}
	if wantsLogLabelMeal("Это завтрак?") || wantsLogLabelMeal("Сохрани продукт") {
		t.Fatal("question or catalogue request must not create a meal")
	}
}

func TestMealStatusAndConfirmationRouting(t *testing.T) {
	if !isMealStatusQuestion("Это пошло в сегодняшние приемы пищи?") {
		t.Fatal("diary status question was not routed")
	}
	if !isMealAffirmation("Ну да") {
		t.Fatal("confirmation was not recognized")
	}
	if isMealAffirmation("Как дела?") {
		t.Fatal("unrelated message was treated as consent to log")
	}
}
