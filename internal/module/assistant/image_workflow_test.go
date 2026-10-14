package assistant

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jasmine/internal/clients/openrouter"
)

func TestImageClassificationCategories(t *testing.T) {
	for _, kind := range []ImageType{ImageFood, ImageNutritionLabel, ImageGymDisplay, ImageScale, ImageBodyProgress, ImageFitnessEquipment, ImageDocument, ImageOther, ImageUnknown} {
		raw := `{"type":"` + string(kind) + `","confidence":0.91,"evidence":["visible"],"requires_clarification":false}`
		d, err := DecodeClassification(raw)
		if err != nil || d.Type != kind {
			t.Fatalf("%s: %+v %v", kind, d, err)
		}
	}
	if _, err := DecodeClassification(`{"type":"medical_diagnosis","confidence":1}`); err == nil {
		t.Fatal("unknown image type accepted")
	}
}
func TestFoodDraftValidationAndRecomputedTotal(t *testing.T) {
	d := &FoodAnalysisDraft{Items: []FoodAnalysisItem{{NameRU: "Яйцо", WeightG: 50, Calories: 75, ProteinG: 6, FatG: 5, CarbsG: 1}}, Total: FoodAnalysisTotal{Calories: 9999}}
	if err := ValidateFoodDraft(d); err != nil {
		t.Fatal(err)
	}
	if d.Total.Calories != 75 || d.Total.ProteinG != 6 {
		t.Fatalf("total was not recomputed: %+v", d.Total)
	}
	d.Items[0].Calories = -1
	if ValidateFoodDraft(d) == nil {
		t.Fatal("negative calorie accepted")
	}
	d.Items[0].Calories = 75
	d.Items[0].WeightG = 0
	if ValidateFoodDraft(d) == nil {
		t.Fatal("zero weight accepted")
	}
}
func TestImageDateAndGymUnits(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, loc)
	for _, tc := range []struct{ caption, day string }{{"это вчера", "2026-09-22"}, {"позавчера", "2026-09-21"}, {"за понедельник", "2026-09-21"}, {"за 18 сентября", "2026-09-18"}} {
		at, explicit, err := resolveImageTimeAt(now, tc.caption)
		if err != nil || !explicit || at.Format("2006-01-02") != tc.day {
			t.Fatalf("%s -> %v %v %v", tc.caption, at, explicit, err)
		}
	}
	text := "01:02:03"
	miles := 3.0
	wrongSeconds := 1
	wrongKM := 1.0
	d := &GymMachineDisplayDraft{DurationText: &text, DurationSeconds: &wrongSeconds, DistanceValue: &miles, DistanceUnit: "mi", DistanceKM: &wrongKM, Confidence: 0.9}
	if err := NormalizeGymDraft(d); err != nil {
		t.Fatal(err)
	}
	if *d.DurationSeconds != 3723 || math.Abs(*d.DistanceKM-4.828032) > 0.001 {
		t.Fatalf("wrong normalized metrics: %+v", d)
	}
}

type fakeLookup struct {
	calls int
	match *NutritionProductMatch
}

func (f *fakeLookup) Lookup(context.Context, string, string, string, *float64) (*NutritionProductMatch, error) {
	f.calls++
	return f.match, nil
}
func visionTestClient(t *testing.T, response string) *openrouter.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []openrouter.ContentPart `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request: %v", err)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 || !strings.HasPrefix(body.Messages[0].Content[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Error("missing multipart image")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": response}}}})
	}))
	t.Cleanup(server.Close)
	return openrouter.NewClientWithBaseURL("test", "vision", server.URL, server.Client())
}
func TestLabelPrintedWinsAndLookupFallback(t *testing.T) {
	img := ImageAttachment{MIMEType: "image/jpeg", Bytes: []byte{0xff, 0xd8, 0xff}}
	printed := `{"product_name":"Батончик","barcode":"12345678","values_basis":"per_100g","calories":210,"protein_g":18,"fat_g":8,"carbs_g":16,"ingredients":["овёс"],"needs_web_lookup":true,"confidence":0.9}`
	analyzer := NewImageAnalyzer(visionTestClient(t, printed), "vision", "main", "reply")
	lookup := &fakeLookup{match: &NutritionProductMatch{Calories: 900, ProteinG: 1, FatG: 1, CarbsG: 1, Source: "open_food_facts", Confidence: 1}}
	analyzer.SetProductLookup(lookup)
	d, err := analyzer.Label(context.Background(), MultimodalTurn{Attachment: img})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "printed_label" || *d.Calories != 210 || lookup.calls != 0 {
		t.Fatalf("printed value not preferred: %+v lookup=%d", d, lookup.calls)
	}
	missing := `{"product_name":"Батончик","barcode":"12345678","values_basis":"unknown","calories":null,"protein_g":null,"fat_g":null,"carbs_g":null,"ingredients":[],"needs_web_lookup":true,"confidence":0.8}`
	analyzer = NewImageAnalyzer(visionTestClient(t, missing), "vision", "main", "reply")
	analyzer.SetProductLookup(lookup)
	d, err = analyzer.Label(context.Background(), MultimodalTurn{Attachment: img})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "open_food_facts" || lookup.calls != 1 {
		t.Fatalf("lookup not used: %+v", d)
	}
}
func TestScaleLBConversionAndValidation(t *testing.T) {
	analyzer := NewImageAnalyzer(visionTestClient(t, `{"weight_kg":null,"weight_lb":160,"body_fat_percent":null,"confidence":0.99}`), "vision", "main", "reply")
	d, err := analyzer.Scale(context.Background(), MultimodalTurn{Attachment: ImageAttachment{MIMEType: "image/jpeg", Bytes: []byte{1}}})
	if err != nil {
		t.Fatal(err)
	}
	if d.WeightKG == nil || math.Abs(*d.WeightKG-72.5747792) > 0.001 || d.BodyFatPercent != nil {
		t.Fatalf("invalid scale: %+v", d)
	}
}

func TestLabelPartialPrintedNeverOverwrittenAndEstimateLast(t *testing.T) {
	img := ImageAttachment{MIMEType: "image/jpeg", Bytes: []byte{0xff, 0xd8, 0xff}}
	partial := `{"product_name":"Творог","barcode":"12345678","values_basis":"per_100g","calories":110,"protein_g":null,"fat_g":null,"carbs_g":null,"ingredients":["молоко"],"needs_web_lookup":true,"confidence":0.9}`
	lookup := &fakeLookup{match: &NutritionProductMatch{Calories: 500, ProteinG: 1, FatG: 1, CarbsG: 1, Confidence: 0.99}}
	a := NewImageAnalyzer(visionTestClient(t, partial), "vision", "main", "reply")
	a.SetProductLookup(lookup)
	d, err := a.Label(context.Background(), MultimodalTurn{Attachment: img})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "printed_label" || *d.Calories != 110 || lookup.calls != 0 || d.ProteinG != nil {
		t.Fatalf("partial print overwritten: %+v", d)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		answer := `{"product_name":"Домашний продукт","values_basis":"unknown","calories":null,"protein_g":null,"fat_g":null,"carbs_g":null,"ingredients":["овёс","масло"],"needs_web_lookup":false,"confidence":0.7}`
		if req.Model == "main" {
			if len(req.Messages) == 0 || strings.Contains(string(req.Messages[0].Content), "image_url") {
				t.Error("ingredient estimate must be text-only")
			}
			answer = `{"calories":300,"protein_g":10,"fat_g":12,"carbs_g":38,"confidence":0.3}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
	}))
	defer server.Close()
	a = NewImageAnalyzer(openrouter.NewClientWithBaseURL("test", "vision", server.URL, server.Client()), "vision", "main", "reply")
	d, err = a.Label(context.Background(), MultimodalTurn{Attachment: img})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "estimated_from_ingredients" || d.Confidence > 0.4 || d.Calories == nil {
		t.Fatalf("missing rough fallback: %+v", d)
	}
}

func TestImageAnswerUsesPhotoThenReplyModel(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		models = append(models, body.Model)
		if body.Model == "photo-model" {
			if len(body.Messages) != 1 || !strings.Contains(string(body.Messages[0]), "image_url") {
				t.Error("photo model did not receive the image")
			}
		} else if body.Model == "reply-model" {
			for _, msg := range body.Messages {
				if strings.Contains(string(msg), "image_url") {
					t.Error("reply model received the image")
				}
			}
			if !strings.Contains(string(body.Messages[len(body.Messages)-1]), "контейнер") {
				t.Error("reply model did not receive photo observation")
			}
		} else {
			t.Errorf("unexpected model %q", body.Model)
		}
		answer := `{"visible_volume_marks":["0.6"],"container_shape":"round","remaining_food":"small","volume_estimate_ml":null,"uncertainties":[]}`
		if body.Model == "reply-model" {
			answer = "Да, по новому ракурсу порция кажется больше."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
	}))
	defer server.Close()
	client := openrouter.NewClientWithBaseURL("test", "main-model", server.URL, server.Client())
	analyzer := NewImageAnalyzer(client, "photo-model", "main-model", "reply-model")
	answer, err := analyzer.MealFollowup(context.Background(), MultimodalTurn{Text: "Думаешь 570?", Attachment: ImageAttachment{MIMEType: "image/jpeg", Bytes: []byte{0xff, 0xd8, 0xff}}})
	if err != nil || !strings.Contains(answer, "visible_volume_marks") {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if len(models) != 1 || models[0] != "photo-model" {
		t.Fatalf("wrong model sequence: %v", models)
	}
}
