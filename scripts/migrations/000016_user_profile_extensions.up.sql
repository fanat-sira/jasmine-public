ALTER TABLE users
    ADD COLUMN age SMALLINT,
    ADD COLUMN avatar_data TEXT,
    ADD COLUMN units TEXT NOT NULL DEFAULT 'metric',
    ADD COLUMN default_rest_sec SMALLINT NOT NULL DEFAULT 120;

ALTER TABLE users
    ADD CONSTRAINT users_units_check CHECK (units IN ('metric', 'imperial')),
    ADD CONSTRAINT users_default_rest_sec_check CHECK (default_rest_sec >= 15 AND default_rest_sec <= 600);

CREATE TABLE user_custom_measurements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    value_text TEXT NOT NULL,
    unit TEXT NOT NULL,
    measured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_user_custom_measurements_user_created
    ON user_custom_measurements (user_id, created_at DESC);
