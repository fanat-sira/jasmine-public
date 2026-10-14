package prompts

import (
	"context"
	"strings"
	"testing"
)

type promptStore map[string]string

func (s promptStore) GetPromptTemplate(_ context.Context, key string) (string, error) {
	return s[key], nil
}

func TestActivePromptCatalogAndOverrides(t *testing.T) {
	keys := []string{
		KeyJasminePlanner, KeyCoachPlanner, KeyJasmineReply, KeyWorkoutProposal,
		KeyImageRouter, KeyFoodImage, KeyNutritionLabel, KeyNutritionEstimate,
		KeyScale, KeyGymDisplay, KeyEquipment, KeyBodyProgress, KeyDocumentImage,
		KeyOtherImage, KeyMealFollowup, KeyMemoryExtraction, KeyProfileDocument, KeyWorkoutExtraction,
		KeyPersonalFactExtraction, KeyAlbumInterpretation, KeyMealPortionEstimate,
		KeyCorrectionExtraction, KeyExerciseResolution, KeyWorkoutCommand,
		KeyWorkoutGeneration, KeyFoodRecognition,
	}
	if len(keys) != len(Defaults) {
		t.Fatalf("catalog entries=%d, enumerated=%d", len(Defaults), len(keys))
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("duplicate key %s", key)
		}
		seen[key] = true
		base := strings.TrimSpace(Default(key))
		if base == "" {
			t.Fatalf("missing default for %s", key)
		}
		got, err := Resolve(context.Background(), promptStore{}, key)
		if err != nil || got != Default(key) {
			t.Fatalf("default %s: %q %v", key, got, err)
		}
		got, err = Resolve(context.Background(), promptStore{key: "DB_OVERRIDE"}, key)
		if err != nil || got != "DB_OVERRIDE" {
			t.Fatalf("override %s: %q %v", key, got, err)
		}
	}
	if _, err := Resolve(context.Background(), nil, "unregistered"); err == nil {
		t.Fatal("unregistered prompt accepted")
	}
}
