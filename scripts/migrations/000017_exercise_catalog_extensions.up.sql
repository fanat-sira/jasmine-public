-- 000017_exercise_catalog_extensions.up.sql
-- Extends exercises table for rich bilingual catalog import.

ALTER TABLE exercises
    ADD COLUMN IF NOT EXISTS exercise_code VARCHAR(150),
    ADD COLUMN IF NOT EXISTS name_en VARCHAR(150),
    ADD COLUMN IF NOT EXISTS name_ru VARCHAR(150),
    ADD COLUMN IF NOT EXISTS aliases_en TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS aliases_ru TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS category VARCHAR(50),
    ADD COLUMN IF NOT EXISTS body_region VARCHAR(50),
    ADD COLUMN IF NOT EXISTS movement_pattern VARCHAR(50),
    ADD COLUMN IF NOT EXISTS equipment TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS environment TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS primary_muscles TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS secondary_muscles TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS difficulty VARCHAR(30),
    ADD COLUMN IF NOT EXISTS technical_complexity SMALLINT,
    ADD COLUMN IF NOT EXISTS impact_level VARCHAR(30),
    ADD COLUMN IF NOT EXISTS is_unilateral BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS is_cardio BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS tracking_type VARCHAR(50),
    ADD COLUMN IF NOT EXISTS score_weight SMALLINT,
    ADD COLUMN IF NOT EXISTS fatigue_cost SMALLINT,
    ADD COLUMN IF NOT EXISTS injury_risk VARCHAR(30),
    ADD COLUMN IF NOT EXISTS notes TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source VARCHAR(30) NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE exercises
SET exercise_code = name
WHERE COALESCE(exercise_code, '') = '';

UPDATE exercises
SET name_en = name
WHERE COALESCE(name_en, '') = '';

UPDATE exercises
SET name_ru = name
WHERE COALESCE(name_ru, '') = '';

ALTER TABLE exercises
    ALTER COLUMN exercise_code SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_exercises_exercise_code
    ON exercises(exercise_code);

CREATE INDEX IF NOT EXISTS idx_exercises_name_en
    ON exercises(name_en);

CREATE INDEX IF NOT EXISTS idx_exercises_name_ru
    ON exercises(name_ru);

CREATE INDEX IF NOT EXISTS idx_exercises_category
    ON exercises(category);

CREATE INDEX IF NOT EXISTS idx_exercises_body_region
    ON exercises(body_region);
