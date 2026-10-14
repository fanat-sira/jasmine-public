CREATE TABLE water_intake (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount_ml  INT         NOT NULL CHECK (amount_ml > 0),
    drank_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_water_intake_user_date ON water_intake (user_id, drank_at);
