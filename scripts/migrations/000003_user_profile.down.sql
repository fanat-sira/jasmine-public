ALTER TABLE users
    DROP COLUMN IF EXISTS weight_kg,
    DROP COLUMN IF EXISTS height_cm,
    DROP COLUMN IF EXISTS target_calories;
