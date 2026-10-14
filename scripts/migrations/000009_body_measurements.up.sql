CREATE TABLE body_measurements (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    logged_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    -- Total body
    weight_kg  DECIMAL(6,2),
    bmi        DECIMAL(5,2),
    bmr_kcal   DECIMAL(7,2),

    -- Composition: fat
    fat_total_kg    DECIMAL(6,2),
    fat_percentage  DECIMAL(5,2),
    visceral_level  INT,

    -- Composition: muscle
    muscle_total_kg  DECIMAL(6,2),
    muscle_index     DECIMAL(5,2),

    -- Composition: water
    water_total_kg   DECIMAL(6,2),
    water_percentage DECIMAL(5,2),

    -- Composition: minerals & protein
    minerals_total_kg DECIMAL(6,3),
    protein_total_kg  DECIMAL(6,2),

    -- Segments: torso
    torso_fat_kg  DECIMAL(6,2),
    torso_lean_kg DECIMAL(6,2),

    -- Segments: arms
    arms_left_fat_kg   DECIMAL(5,2),
    arms_left_lean_kg  DECIMAL(5,2),
    arms_right_fat_kg  DECIMAL(5,2),
    arms_right_lean_kg DECIMAL(5,2),

    -- Segments: legs
    legs_left_fat_kg   DECIMAL(5,2),
    legs_left_lean_kg  DECIMAL(5,2),
    legs_right_fat_kg  DECIMAL(5,2),
    legs_right_lean_kg DECIMAL(5,2),

    -- Ratios
    waist_hip_ratio DECIMAL(4,2)
);

CREATE INDEX idx_body_measurements_user_logged ON body_measurements (user_id, logged_at DESC);
