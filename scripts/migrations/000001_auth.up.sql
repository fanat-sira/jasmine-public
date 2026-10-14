-- Расширение для UUID (pgcrypto даёт gen_random_uuid())
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── users ────────────────────────────────────────────────────────────────────
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         VARCHAR(255) NOT NULL,
    username      VARCHAR(50)  NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT users_email_unique    UNIQUE (email),
    CONSTRAINT users_username_unique UNIQUE (username)
);

CREATE INDEX idx_users_email    ON users (email);
CREATE INDEX idx_users_username ON users (username);

-- ── invite_codes ─────────────────────────────────────────────────────────────
CREATE TABLE invite_codes (
    code       VARCHAR(64)  PRIMARY KEY,
    used_by    UUID         REFERENCES users(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ  NOT NULL
);

-- ── refresh_tokens ───────────────────────────────────────────────────────────
-- Храним refresh-токены в БД для возможности их инвалидации (logout, force-revoke).
CREATE TABLE refresh_tokens (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT refresh_tokens_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens (user_id);
