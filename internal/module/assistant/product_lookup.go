package assistant

import (
	"jasmine/internal/clients/off"
	"context"
	"regexp"
	"strings"
)

// OFFBarcodeLookup is a provider adapter. It accepts only exact barcode requests;
// photographed manufacturer values always take precedence over lookup data.
var productBarcodePattern = regexp.MustCompile(`^[0-9]{8,14}$`)

type OFFBarcodeLookup struct{ Client *off.Client }

func (l OFFBarcodeLookup) Lookup(ctx context.Context, barcode, brand, product string, packageWeightG *float64) (*NutritionProductMatch, error) {
	if l.Client == nil || !productBarcodePattern.MatchString(strings.TrimSpace(barcode)) {
		return nil, nil
	}
	p, err := l.Client.GetByBarcode(ctx, strings.TrimSpace(barcode))
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, nil
	}
	return &NutritionProductMatch{ProductName: p.Name, Calories: p.Calories, ProteinG: p.Proteins, FatG: p.Fats, CarbsG: p.Carbs, Source: "open_food_facts", Confidence: 0.85}, nil
}
