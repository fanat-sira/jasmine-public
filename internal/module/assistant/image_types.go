package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type ImageType string

const (
	ImageFood             ImageType = "food"
	ImageNutritionLabel   ImageType = "nutrition_label"
	ImageGymDisplay       ImageType = "gym_machine_display"
	ImageScale            ImageType = "scale"
	ImageBodyProgress     ImageType = "body_progress"
	ImageFitnessEquipment ImageType = "fitness_equipment"
	ImageDocument         ImageType = "document"
	ImageOther            ImageType = "other"
	ImageUnknown          ImageType = "unknown"
)

type ImageAttachment struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	MIMEType     string `json:"mime_type"`
	ByteSize     int64  `json:"byte_size"`
	Bytes        []byte `json:"-"`
}
type MultimodalTurn struct {
	Text                string               `json:"text"`
	ConversationContext string               `json:"conversation_context,omitempty"`
	Timezone            string               `json:"timezone,omitempty"`
	RecentMessages      []CoachRecentMessage `json:"-"`
	Attachment          ImageAttachment      `json:"attachment"`
	UpdateID            int64                `json:"update_id"`
	MessageID           int64                `json:"message_id"`
	CorrelationID       string               `json:"correlation_id"`
}
type ImageClassificationDraft struct {
	Type                  ImageType `json:"type"`
	Confidence            float64   `json:"confidence"`
	Evidence              []string  `json:"evidence"`
	RequiresClarification bool      `json:"requires_clarification"`
}
type FoodAnalysisDraft struct {
	Items []FoodAnalysisItem `json:"items"`
	Total FoodAnalysisTotal  `json:"total"`
	Notes string             `json:"notes"`
}
type FoodAnalysisItem struct {
	NameRU     string  `json:"name_ru"`
	NameEN     string  `json:"name_en"`
	WeightG    float64 `json:"weight_g"`
	Calories   float64 `json:"calories"`
	ProteinG   float64 `json:"protein_g"`
	FatG       float64 `json:"fat_g"`
	CarbsG     float64 `json:"carbs_g"`
	Confidence string  `json:"confidence"`
}
type FoodAnalysisTotal struct {
	Calories float64 `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	FatG     float64 `json:"fat_g"`
	CarbsG   float64 `json:"carbs_g"`
}
type NutritionLabelDraft struct {
	ProductName    *string  `json:"product_name"`
	Brand          *string  `json:"brand"`
	Barcode        *string  `json:"barcode"`
	PackageWeightG *float64 `json:"package_weight_g"`
	ServingSizeG   *float64 `json:"serving_size_g"`
	ValuesBasis    string   `json:"values_basis"`
	Calories       *float64 `json:"calories"`
	ProteinG       *float64 `json:"protein_g"`
	FatG           *float64 `json:"fat_g"`
	CarbsG         *float64 `json:"carbs_g"`
	Ingredients    []string `json:"ingredients"`
	NeedsWebLookup bool     `json:"needs_web_lookup"`
	Source         string   `json:"source,omitempty"`
	Confidence     float64  `json:"confidence"`
}
type GymMachineDisplayDraft struct {
	MachineHint     *string  `json:"machine_hint"`
	DurationText    *string  `json:"duration_text,omitempty"`
	DistanceValue   *float64 `json:"distance_value,omitempty"`
	DistanceUnit    string   `json:"distance_unit,omitempty"`
	SpeedValue      *float64 `json:"speed_value,omitempty"`
	SpeedUnit       string   `json:"speed_unit,omitempty"`
	Resistance      *int     `json:"resistance,omitempty"`
	Level           *int     `json:"level,omitempty"`
	InclinePercent  *float64 `json:"incline_percent,omitempty"`
	Watts           *float64 `json:"watts,omitempty"`
	RPM             *int     `json:"rpm,omitempty"`
	Steps           *int     `json:"steps,omitempty"`
	Floors          *int     `json:"floors,omitempty"`
	Strokes         *int     `json:"strokes,omitempty"`
	Repetitions     *int     `json:"repetitions,omitempty"`
	LoadKG          *float64 `json:"load_kg,omitempty"`
	DurationSeconds *int     `json:"duration_seconds"`
	DistanceKM      *float64 `json:"distance_km"`
	Calories        *float64 `json:"calories"`
	HeartRateBPM    *int     `json:"heart_rate_bpm"`
	SpeedKMH        *float64 `json:"speed_kmh"`
	Confidence      float64  `json:"confidence"`
}
type ScaleMeasurementDraft struct {
	WeightKG       *float64 `json:"weight_kg"`
	BodyFatPercent *float64 `json:"body_fat_percent"`
	MuscleMassKG   *float64 `json:"muscle_mass_kg"`
	WaterPercent   *float64 `json:"water_percent"`
	VisceralLevel  *int     `json:"visceral_fat,omitempty"`
	BMI            *float64 `json:"bmi"`
	Confidence     float64  `json:"confidence"`
}

type NutritionProductLookup interface {
	Lookup(ctx context.Context, barcode, brand, product string, packageWeightG *float64) (*NutritionProductMatch, error)
}
type NutritionProductMatch struct {
	ProductName                      string
	Calories, ProteinG, FatG, CarbsG float64
	Source                           string
	Confidence                       float64
}
type UnavailableProductLookup struct{}

func (UnavailableProductLookup) Lookup(context.Context, string, string, string, *float64) (*NutritionProductMatch, error) {
	return nil, fmt.Errorf("nutrition product lookup is not configured")
}

func DecodeClassification(raw string) (ImageClassificationDraft, error) {
	var d ImageClassificationDraft
	if err := json.Unmarshal([]byte(stripImageFences(raw)), &d); err != nil {
		return d, err
	}
	switch d.Type {
	case ImageFood, ImageNutritionLabel, ImageGymDisplay, ImageScale, ImageBodyProgress, ImageFitnessEquipment, ImageDocument, ImageOther, ImageUnknown:
	default:
		return d, fmt.Errorf("unknown image type %q", d.Type)
	}
	if math.IsNaN(d.Confidence) || math.IsInf(d.Confidence, 0) || d.Confidence < 0 || d.Confidence > 1 {
		return d, fmt.Errorf("invalid confidence")
	}
	return d, nil
}
func ValidateFoodDraft(d *FoodAnalysisDraft) error {
	if d == nil || len(d.Items) == 0 || len(d.Items) > 30 {
		return fmt.Errorf("food items are required")
	}
	var c, p, f, car float64
	for _, v := range d.Items {
		if strings.TrimSpace(v.NameRU) == "" && strings.TrimSpace(v.NameEN) == "" {
			return fmt.Errorf("food item name is required")
		}
		if v.WeightG < 1 || v.WeightG > 5000 {
			return fmt.Errorf("invalid food item weight")
		}
		for _, n := range []float64{v.Calories, v.ProteinG, v.FatG, v.CarbsG} {
			if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 10000 {
				return fmt.Errorf("invalid food value")
			}
		}
		if v.Calories > v.WeightG*10 || v.ProteinG > v.WeightG*1.2 || v.FatG > v.WeightG*1.2 || v.CarbsG > v.WeightG*1.2 {
			return fmt.Errorf("food item nutrition is implausible")
		}
		c += v.Calories
		p += v.ProteinG
		f += v.FatG
		car += v.CarbsG
	}
	if c > 10000 || p > 2000 || f > 2000 || car > 3000 {
		return fmt.Errorf("food total is implausible")
	}
	d.Total = FoodAnalysisTotal{c, p, f, car}
	return nil
}
func stripImageFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

func validateOptional(n *float64, max float64) error {
	if n == nil {
		return nil
	}
	if math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0 || *n > max {
		return fmt.Errorf("invalid numeric observation")
	}
	return nil
}
func ValidateScaleDraft(d *ScaleMeasurementDraft) error {
	if d == nil || math.IsNaN(d.Confidence) || d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("invalid scale confidence")
	}
	for _, v := range []struct {
		value *float64
		max   float64
	}{{d.WeightKG, 600}, {d.BodyFatPercent, 100}, {d.MuscleMassKG, 300}, {d.WaterPercent, 100}, {d.BMI, 100}} {
		if err := validateOptional(v.value, v.max); err != nil {
			return err
		}
	}
	if d.VisceralLevel != nil && (*d.VisceralLevel < 0 || *d.VisceralLevel > 100) {
		return fmt.Errorf("invalid visceral fat level")
	}
	if d.WeightKG != nil && *d.WeightKG < 2 {
		return fmt.Errorf("implausible body weight")
	}
	return nil
}
func ValidateLabelDraft(d *NutritionLabelDraft) error {
	if d == nil || math.IsNaN(d.Confidence) || d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("invalid label confidence")
	}
	switch d.ValuesBasis {
	case "per_100g", "per_serving", "per_package", "unknown":
	default:
		return fmt.Errorf("invalid values basis")
	}
	for _, v := range []*float64{d.Calories, d.ProteinG, d.FatG, d.CarbsG} {
		if err := validateOptional(v, 10000); err != nil {
			return err
		}
	}
	for _, v := range []*float64{d.PackageWeightG, d.ServingSizeG} {
		if err := validateOptional(v, 100000); err != nil {
			return err
		}
	}
	return nil
}
func ValidateGymDraft(d *GymMachineDisplayDraft) error {
	if d == nil || math.IsNaN(d.Confidence) || d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("invalid gym confidence")
	}
	if d.DurationSeconds != nil && (*d.DurationSeconds < 0 || *d.DurationSeconds > 86400) {
		return fmt.Errorf("invalid gym duration")
	}
	for _, v := range []struct {
		value *float64
		max   float64
	}{{d.DistanceKM, 1000}, {d.Calories, 10000}, {d.SpeedKMH, 150}} {
		if err := validateOptional(v.value, v.max); err != nil {
			return err
		}
	}
	for _, v := range []struct {
		value *float64
		max   float64
	}{{d.InclinePercent, 100}, {d.Watts, 5000}, {d.LoadKG, 1000}} {
		if err := validateOptional(v.value, v.max); err != nil {
			return err
		}
	}
	for _, v := range []*int{d.Resistance, d.Level, d.RPM, d.Steps, d.Floors, d.Strokes, d.Repetitions} {
		if v != nil && (*v < 0 || *v > 1000000) {
			return fmt.Errorf("invalid gym metric")
		}
	}
	if d.HeartRateBPM != nil && (*d.HeartRateBPM < 0 || *d.HeartRateBPM > 300) {
		return fmt.Errorf("invalid heart rate")
	}
	return nil
}

type EquipmentIdentificationDraft struct {
	Name       string  `json:"name"`
	Purpose    string  `json:"purpose"`
	Confidence float64 `json:"confidence"`
}

func NormalizeGymDraft(d *GymMachineDisplayDraft) error {
	if d == nil {
		return fmt.Errorf("gym draft is nil")
	}
	if d.DurationText != nil {
		parts := strings.Split(strings.TrimSpace(*d.DurationText), ":")
		if len(parts) != 2 && len(parts) != 3 {
			return fmt.Errorf("invalid duration display")
		}
		seconds := 0
		for _, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n > 59 {
				return fmt.Errorf("invalid duration display")
			}
			seconds = seconds*60 + n
		}
		if len(parts) == 2 && seconds > 86400 {
			return fmt.Errorf("duration too long")
		}
		d.DurationSeconds = &seconds
	}
	if d.DistanceValue != nil {
		if err := validateOptional(d.DistanceValue, 1000); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(d.DistanceUnit)) {
		case "km", "км":
			v := *d.DistanceValue
			d.DistanceKM = &v
		case "mi", "mile", "miles", "миль":
			v := *d.DistanceValue * 1.609344
			d.DistanceKM = &v
		case "m", "метр", "метров":
			v := *d.DistanceValue / 1000
			d.DistanceKM = &v
		default:
			return fmt.Errorf("unknown distance unit")
		}
	}
	if d.SpeedValue != nil {
		if err := validateOptional(d.SpeedValue, 150); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(d.SpeedUnit)) {
		case "km/h", "км/ч":
			v := *d.SpeedValue
			d.SpeedKMH = &v
		case "mph", "mi/h":
			v := *d.SpeedValue * 1.609344
			d.SpeedKMH = &v
		default:
			return fmt.Errorf("unknown speed unit")
		}
	}
	return ValidateGymDraft(d)
}
