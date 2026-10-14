-- 000010_exercises_refactor.up.sql

-- 1. Global exercise catalogue
CREATE TABLE exercises (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(150) UNIQUE NOT NULL,
    type         VARCHAR(50)  NOT NULL,          -- 'strength' | 'cardio'
    muscle_group VARCHAR(100) NOT NULL,
    is_default   BOOLEAN      NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- 2. Junction: one workout → many exercises, each with JSONB sets
CREATE TABLE workout_exercises (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workout_id  UUID        NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    exercise_id UUID        NOT NULL REFERENCES exercises(id),
    order_index INT         NOT NULL DEFAULT 0,
    notes       TEXT,
    sets_data   JSONB       NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_workout_exercises_workout_id ON workout_exercises(workout_id);

-- 3. Drop the old JSONB column from workouts
ALTER TABLE workouts DROP COLUMN IF EXISTS exercises;

-- Exercise catalogue is populated via API/import flows (no hardcoded seed rows).
