-- 000017_exercise_catalog_extensions.down.sql

DROP INDEX IF EXISTS idx_exercises_body_region;
DROP INDEX IF EXISTS idx_exercises_category;
DROP INDEX IF EXISTS idx_exercises_name_ru;
DROP INDEX IF EXISTS idx_exercises_name_en;
DROP INDEX IF EXISTS idx_exercises_exercise_code;

ALTER TABLE exercises
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS source,
    DROP COLUMN IF EXISTS notes,
    DROP COLUMN IF EXISTS injury_risk,
    DROP COLUMN IF EXISTS fatigue_cost,
    DROP COLUMN IF EXISTS score_weight,
    DROP COLUMN IF EXISTS tracking_type,
    DROP COLUMN IF EXISTS is_cardio,
    DROP COLUMN IF EXISTS is_unilateral,
    DROP COLUMN IF EXISTS impact_level,
    DROP COLUMN IF EXISTS technical_complexity,
    DROP COLUMN IF EXISTS difficulty,
    DROP COLUMN IF EXISTS secondary_muscles,
    DROP COLUMN IF EXISTS primary_muscles,
    DROP COLUMN IF EXISTS environment,
    DROP COLUMN IF EXISTS equipment,
    DROP COLUMN IF EXISTS movement_pattern,
    DROP COLUMN IF EXISTS body_region,
    DROP COLUMN IF EXISTS category,
    DROP COLUMN IF EXISTS aliases_ru,
    DROP COLUMN IF EXISTS aliases_en,
    DROP COLUMN IF EXISTS name_ru,
    DROP COLUMN IF EXISTS name_en,
    DROP COLUMN IF EXISTS exercise_code;
