package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"jasmine/internal/module/prompts"
	"jasmine/internal/pkg/correlation"
)

func (o *Orchestrator) estimateMealPortion(ctx context.Context, old any, statement string, volumeML *float64) (MealMutation, error) {
	if o.aiClient == nil || o.model == "" {
		return MealMutation{}, fmt.Errorf("main model unavailable for portion estimate")
	}
	input, _ := json.Marshal(map[string]any{"existing_item": old, "user_statement": statement, "container_volume_ml": volumeML})
	prompt, err := prompts.Resolve(ctx, o.prompts, prompts.KeyMealPortionEstimate)
	if err != nil {
		return MealMutation{}, err
	}
	aiCtx, cancel := context.WithTimeout(correlation.WithOperation(ctx, "meal_portion_refinement"), 35*time.Second)
	defer cancel()
	raw, err := o.aiClient.SendConversation(aiCtx, o.model, prompt, nil, string(input))
	if err != nil {
		return MealMutation{}, err
	}
	var result MealMutation
	if err := json.Unmarshal([]byte(stripMarkdownFences(raw)), &result); err != nil {
		return MealMutation{}, err
	}
	result.Operation = "refine_portion"
	if result.WeightG == nil || result.Calories == nil || *result.WeightG < 1 || *result.WeightG > 5000 || *result.Calories < 1 || *result.Calories > *result.WeightG*10 {
		return MealMutation{}, fmt.Errorf("portion estimate incomplete or implausible")
	}
	return result, nil
}
