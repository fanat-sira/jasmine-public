CREATE TABLE workouts (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    started_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    duration_min INT          NOT NULL DEFAULT 0,
    workout_type VARCHAR(30)  NOT NULL DEFAULT 'strength',
    total_volume DECIMAL(12,2) NOT NULL DEFAULT 0,
    exercises    JSONB        NOT NULL DEFAULT '[]',
    notes        TEXT,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_workouts_user_started ON workouts (user_id, started_at DESC);
