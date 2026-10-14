CREATE TABLE meal_entries (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id UUID         NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    meal_type  VARCHAR(20)  NOT NULL CHECK (meal_type IN ('breakfast','lunch','dinner','snack')),
    weight_g   DECIMAL(8,2) NOT NULL,
    calories   DECIMAL(8,2) NOT NULL DEFAULT 0,
    proteins   DECIMAL(8,2) NOT NULL DEFAULT 0,
    fats       DECIMAL(8,2) NOT NULL DEFAULT 0,
    carbs      DECIMAL(8,2) NOT NULL DEFAULT 0,
    eaten_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_meal_entries_user_date ON meal_entries (user_id, eaten_at);
