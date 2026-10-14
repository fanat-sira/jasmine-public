package diary

import "time"

type MealEntry struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	ProductID   string    `json:"product_id"`
	ProductName string    `json:"product_name,omitempty"`
	MealType    string    `json:"meal_type"`
	WeightG     float64   `json:"weight_g"`
	Calories    float64   `json:"calories"`
	Proteins    float64   `json:"proteins"`
	Fats        float64   `json:"fats"`
	Carbs       float64   `json:"carbs"`
	EatenAt     time.Time `json:"eaten_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type Macros struct {
	Calories float64 `json:"calories"`
	Proteins float64 `json:"proteins"`
	Fats     float64 `json:"fats"`
	Carbs    float64 `json:"carbs"`
	WaterMl  int     `json:"water_ml"`
}
