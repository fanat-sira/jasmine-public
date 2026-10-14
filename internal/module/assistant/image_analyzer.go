package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/prompts"
	"jasmine/internal/pkg/correlation"
)

type ImageAnalyzer struct {
	client     *openrouter.Client
	model      string
	mainModel  string
	replyModel string
	lookup     NutritionProductLookup
	prompts    ImagePromptStore
}

func NewImageAnalyzer(c *openrouter.Client, photoModel, mainModel, replyModel string) *ImageAnalyzer {
	return &ImageAnalyzer{client: c, model: photoModel, mainModel: mainModel, replyModel: replyModel, lookup: UnavailableProductLookup{}}
}

type ImagePromptStore = prompts.Store

func (a *ImageAnalyzer) SetPromptStore(store ImagePromptStore) { a.prompts = store }

func (a *ImageAnalyzer) SetProductLookup(l NutritionProductLookup) {
	if l != nil {
		a.lookup = l
	}
}
func (a *ImageAnalyzer) call(ctx context.Context, operation, prompt string, in ImageAttachment) (string, error) {
	return a.callWithSystem(ctx, operation, prompt, in)
}

func (a *ImageAnalyzer) callWithSystem(ctx context.Context, operation, prompt string, in ImageAttachment) (string, error) {
	if a == nil || a.client == nil {
		return "", fmt.Errorf("vision AI is not configured")
	}
	if len(in.Bytes) == 0 {
		return "", fmt.Errorf("image is empty")
	}
	mime := in.MIMEType
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return "", fmt.Errorf("unsupported image type")
	}
	if base := prompts.Default(operation); base != "" {
		resolved, err := prompts.Resolve(ctx, a.prompts, operation)
		if err != nil {
			return "", err
		}
		prompt = strings.Replace(prompt, base, resolved, 1)
	}
	ctx = correlation.WithOperation(ctx, operation)
	parts := []openrouter.ContentPart{{Type: "text", Text: prompt}, {Type: "image_url", ImageURL: &openrouter.ImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(in.Bytes)}}}
	return a.client.SendChat(ctx, a.model, parts)
}
func (a *ImageAnalyzer) structured(ctx context.Context, operation, prompt string, in ImageAttachment, out any) error {
	start := time.Now()
	raw, err := a.call(ctx, operation, prompt, in)
	if err != nil {
		return err
	}
	err = json.Unmarshal([]byte(stripImageFences(raw)), out)
	a.client.RecordStructuredValidation(ctx, operation, a.model, time.Since(start).Milliseconds(), err == nil, err)
	return err
}
func (a *ImageAnalyzer) Classify(ctx context.Context, turn MultimodalTurn) (ImageClassificationDraft, error) {
	var d ImageClassificationDraft
	prompt := prompts.Default(prompts.KeyImageRouter) + "\nПодпись: " + turn.Text + "\nКонтекст: " + turn.ConversationContext
	if err := a.structured(ctx, "image_classification", prompt, turn.Attachment, &d); err != nil {
		return d, err
	}
	return DecodeClassification(mustJSON(d))
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func imageContext(prompt string, turn MultimodalTurn) string {
	return prompt + "\nПодпись пользователя: " + turn.Text
}
func (a *ImageAnalyzer) Food(ctx context.Context, turn MultimodalTurn) (*FoodAnalysisDraft, error) {
	var d FoodAnalysisDraft
	if err := a.structured(ctx, "food_image_analysis", imageContext(prompts.Default(prompts.KeyFoodImage), turn), turn.Attachment, &d); err != nil {
		return nil, err
	}
	if err := ValidateFoodDraft(&d); err != nil {
		a.client.RecordStructuredValidation(ctx, "food_image_analysis", a.model, 0, false, err)
		return nil, err
	}
	return &d, nil
}
func (a *ImageAnalyzer) Label(ctx context.Context, turn MultimodalTurn) (*NutritionLabelDraft, error) {
	var d NutritionLabelDraft
	if err := a.structured(ctx, "nutrition_label_analysis", imageContext(prompts.Default(prompts.KeyNutritionLabel), turn), turn.Attachment, &d); err != nil {
		return nil, err
	}
	if err := ValidateLabelDraft(&d); err != nil {
		return nil, err
	}
	if d.Calories != nil && d.ProteinG != nil && d.FatG != nil && d.CarbsG != nil {
		d.Source = "printed_label"
		return &d, nil
	}
	if d.Calories != nil || d.ProteinG != nil || d.FatG != nil || d.CarbsG != nil {
		d.Source = "printed_label"
		return &d, nil
	}
	if d.NeedsWebLookup && d.Barcode != nil && a.lookup != nil {
		brand, product := "", ""
		if d.Brand != nil {
			brand = *d.Brand
		}
		if d.ProductName != nil {
			product = *d.ProductName
		}
		match, err := a.lookup.Lookup(ctx, *d.Barcode, brand, product, d.PackageWeightG)
		if err == nil && match != nil && match.Confidence >= 0.8 {
			d.Calories = &match.Calories
			d.ProteinG = &match.ProteinG
			d.FatG = &match.FatG
			d.CarbsG = &match.CarbsG
			d.ValuesBasis = "per_100g"
			d.Source = match.Source
			d.Confidence = match.Confidence
			return &d, nil
		}
	}
	if len(d.Ingredients) > 0 {
		var estimate struct {
			Calories   *float64 `json:"calories"`
			ProteinG   *float64 `json:"protein_g"`
			FatG       *float64 `json:"fat_g"`
			CarbsG     *float64 `json:"carbs_g"`
			Confidence float64  `json:"confidence"`
		}
		if err := a.estimateIngredients(ctx, d.Ingredients, &estimate); err == nil {
			if validateOptional(estimate.Calories, 10000) == nil && validateOptional(estimate.ProteinG, 10000) == nil && validateOptional(estimate.FatG, 10000) == nil && validateOptional(estimate.CarbsG, 10000) == nil && estimate.Calories != nil && estimate.ProteinG != nil && estimate.FatG != nil && estimate.CarbsG != nil {
				d.Calories = estimate.Calories
				d.ProteinG = estimate.ProteinG
				d.FatG = estimate.FatG
				d.CarbsG = estimate.CarbsG
				d.ValuesBasis = "per_100g"
				d.Source = "estimated_from_ingredients"
				d.Confidence = 0.35
			}
		}
	}
	return &d, nil
}
func (a *ImageAnalyzer) estimateIngredients(ctx context.Context, ingredients []string, out any) error {
	prompt, err := prompts.Resolve(ctx, a.prompts, prompts.KeyNutritionEstimate)
	if err != nil {
		return err
	}
	aiCtx := correlation.WithOperation(ctx, "nutrition_ingredient_estimate")
	started := time.Now()
	raw, err := a.client.SendChat(aiCtx, a.mainModel, prompt+"\nСостав: "+strings.Join(ingredients, ", "))
	if err != nil {
		return err
	}
	err = json.Unmarshal([]byte(stripImageFences(raw)), out)
	a.client.RecordStructuredValidation(aiCtx, "nutrition_ingredient_estimate", a.mainModel, time.Since(started).Milliseconds(), err == nil, err)
	return err
}

func (a *ImageAnalyzer) Scale(ctx context.Context, turn MultimodalTurn) (*ScaleMeasurementDraft, error) {
	var raw struct {
		ScaleMeasurementDraft
		WeightLB *float64 `json:"weight_lb"`
	}
	if err := a.structured(ctx, "scale_analysis", imageContext(prompts.Default(prompts.KeyScale), turn), turn.Attachment, &raw); err != nil {
		return nil, err
	}
	d := raw.ScaleMeasurementDraft
	if d.WeightKG == nil && raw.WeightLB != nil {
		if err := validateOptional(raw.WeightLB, 1300); err != nil {
			return nil, err
		}
		kg := *raw.WeightLB * 0.45359237
		d.WeightKG = &kg
	}
	if err := ValidateScaleDraft(&d); err != nil {
		return nil, err
	}
	return &d, nil
}
func (a *ImageAnalyzer) Gym(ctx context.Context, turn MultimodalTurn) (*GymMachineDisplayDraft, error) {
	var d GymMachineDisplayDraft
	if err := a.structured(ctx, "gym_machine_display_analysis", imageContext(prompts.Default(prompts.KeyGymDisplay), turn), turn.Attachment, &d); err != nil {
		return nil, err
	}
	if err := NormalizeGymDraft(&d); err != nil {
		return nil, err
	}
	return &d, nil
}
func (a *ImageAnalyzer) Equipment(ctx context.Context, turn MultimodalTurn) (*EquipmentIdentificationDraft, error) {
	var d EquipmentIdentificationDraft
	if err := a.structured(ctx, "equipment_image_analysis", imageContext(prompts.Default(prompts.KeyEquipment), turn), turn.Attachment, &d); err != nil {
		return nil, err
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return nil, fmt.Errorf("invalid equipment confidence")
	}
	return &d, nil
}
func (a *ImageAnalyzer) observe(ctx context.Context, operation, visionPrompt string, turn MultimodalTurn) (string, error) {
	observation, err := a.call(ctx, operation, imageContext(visionPrompt, turn), turn.Attachment)
	if err != nil {
		return "", err
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripImageFences(observation)), &parsed); err != nil {
		return "", fmt.Errorf("%s returned invalid observation JSON: %w", operation, err)
	}
	if len(parsed) == 0 {
		return "", fmt.Errorf("%s returned empty observation", operation)
	}
	encoded, err := json.Marshal(parsed)
	return string(encoded), err
}

func (a *ImageAnalyzer) Document(ctx context.Context, turn MultimodalTurn) (string, error) {
	return a.observe(ctx, "document_image_observation", prompts.Default(prompts.KeyDocumentImage), turn)
}
func (a *ImageAnalyzer) Other(ctx context.Context, turn MultimodalTurn) (string, error) {
	return a.observe(ctx, "other_image_observation", prompts.Default(prompts.KeyOtherImage), turn)
}
