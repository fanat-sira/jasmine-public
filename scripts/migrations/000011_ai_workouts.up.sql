-- 000011_ai_workouts.up.sql

-- 1. Goal field on users
ALTER TABLE users ADD COLUMN IF NOT EXISTS goal TEXT;

-- 2. AI-generated workout cache (one row per user — UPSERT strategy)
CREATE TABLE ai_generated_workouts (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workout_data JSONB        NOT NULL,
    model_used   VARCHAR(100) NOT NULL,
    generated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_ai_generated_workouts_user UNIQUE (user_id)
);
