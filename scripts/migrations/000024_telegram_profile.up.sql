ALTER TABLE users ADD COLUMN profile_data JSONB;
ALTER TABLE users ADD COLUMN profile_updated_at TIMESTAMPTZ;

CREATE TABLE meal_calorie_corrections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    meal_group_id UUID NOT NULL,
    previous_calories NUMERIC(8,2) NOT NULL,
    new_calories NUMERIC(8,2) NOT NULL,
    source_update_id BIGINT UNIQUE REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
