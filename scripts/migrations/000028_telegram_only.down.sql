-- Restore only compatibility schema; deleted credentials/tokens are not restored.
ALTER TABLE users
    ADD COLUMN email VARCHAR(255),
    ADD COLUMN password_hash VARCHAR(255),
    ADD COLUMN auth_provider VARCHAR(30) NOT NULL DEFAULT 'local',
    ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD CONSTRAINT users_email_unique UNIQUE (email);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_is_admin ON users(is_admin) WHERE is_admin=TRUE;
ALTER TABLE telegram_users DROP CONSTRAINT telegram_users_user_id_unique;

CREATE TABLE invite_codes (
    code VARCHAR(64) PRIMARY KEY,
    used_by UUID REFERENCES users(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(64) NOT NULL CONSTRAINT refresh_tokens_hash_unique UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE TABLE ai_generated_workouts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE CONSTRAINT uq_ai_generated_workouts_user UNIQUE,
    workout_data JSONB NOT NULL,
    model_used VARCHAR(100) NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
