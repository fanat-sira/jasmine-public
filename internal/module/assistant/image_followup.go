package assistant

import (
	"jasmine/internal/module/prompts"
	"context"
	"strings"
)

func isMealEstimateFollowup(caption string) bool {
	t := strings.ToLower(caption)
	return (strings.Contains(t, "думаешь") && (strings.Contains(t, "ккал") || coachNumber.MatchString(t))) ||
		strings.Contains(t, "вид сбоку") ||
		(strings.Contains(t, "оценк") && strings.Contains(t, "калор"))
}

func (a *ImageAnalyzer) MealFollowup(ctx context.Context, turn MultimodalTurn) (string, error) {
	return a.observe(ctx, "meal_followup_analysis", prompts.Default(prompts.KeyMealFollowup), turn)
}
