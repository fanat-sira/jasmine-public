package off

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	baseURL   = "https://world.openfoodfacts.org/api/v2/product/%s.json"
	userAgent = "Jasmine/1.0 (+https://jasmine.online)"
)

// ErrNotFound is returned when OFF has no record for the given barcode.
var ErrNotFound = errors.New("product not found in Open Food Facts")

// Product holds the fields we care about from an OFF response.
type Product struct {
	Name     string
	Calories float64
	Proteins float64
	Fats     float64
	Carbs    float64
}

// offResponse mirrors the OFF API v2 JSON envelope.
type offResponse struct {
	Status  int `json:"status"` // 1 = found, 0 = not found
	Product struct {
		ProductName   string `json:"product_name"`
		ProductNameEn string `json:"product_name_en"`
		Nutriments    struct {
			EnergyKcal100g    float64 `json:"energy-kcal_100g"`
			Proteins100g      float64 `json:"proteins_100g"`
			Fat100g           float64 `json:"fat_100g"`
			Carbohydrates100g float64 `json:"carbohydrates_100g"`
		} `json:"nutriments"`
	} `json:"product"`
}

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// GetByBarcode fetches product data from Open Food Facts by barcode.
// Returns ErrNotFound if OFF has no entry for this barcode.
func (c *Client) GetByBarcode(ctx context.Context, code string) (*Product, error) {
	url := fmt.Sprintf(baseURL, code)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("off: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("off: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("off: unexpected status %d", resp.StatusCode)
	}

	var data offResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("off: decode response: %w", err)
	}
	if data.Status != 1 {
		return nil, ErrNotFound
	}

	name := data.Product.ProductName
	if name == "" {
		name = data.Product.ProductNameEn
	}
	if name == "" {
		return nil, ErrNotFound
	}

	return &Product{
		Name:     name,
		Calories: data.Product.Nutriments.EnergyKcal100g,
		Proteins: data.Product.Nutriments.Proteins100g,
		Fats:     data.Product.Nutriments.Fat100g,
		Carbs:    data.Product.Nutriments.Carbohydrates100g,
	}, nil
}
