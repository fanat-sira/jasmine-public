-- 000019_telegram_conversational_core.up.sql
-- Establishes Telegram identity mapping, update lifecycle & audit ledger,
-- message history with domain references, first-class workout sessions,
-- persistent conversational state, layered memory & feedback evidence log,
-- and durable Telegram outbox deliveries.

-- 1. Relax legacy auth constraints and add timezone for Telegram-native users
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_provider VARCHAR(30) NOT NULL DEFAULT 'local';
ALTER TABLE users ADD COLUMN IF NOT EXISTS timezone VARCHAR(50) NOT NULL DEFAULT 'UTC';

-- 2. Telegram Users (Actor Identity)
CREATE TABLE IF NOT EXISTS telegram_users (
    telegram_user_id BIGINT PRIMARY KEY,
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    username         VARCHAR(255),
    first_name       VARCHAR(255),
    last_name        VARCHAR(255),
    language_code    VARCHAR(10) NOT NULL DEFAULT 'ru',
    timezone         VARCHAR(50) NOT NULL DEFAULT 'UTC',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_telegram_users_user_id ON telegram_users(user_id);

-- 3. Telegram Updates Idempotency Ledger & Lifecycle
CREATE TABLE IF NOT EXISTS telegram_updates (
    update_id        BIGINT PRIMARY KEY,
    chat_id          BIGINT NOT NULL,
    telegram_user_id BIGINT NOT NULL,
    status           VARCHAR(30) NOT NULL DEFAULT 'received', -- 'received', 'processing', 'completed', 'failed', 'skipped'
    attempts         INT NOT NULL DEFAULT 0,
    locked_at        TIMESTAMPTZ,
    locked_by        VARCHAR(100),
    raw_payload      JSONB NOT NULL,
    response_payload JSONB,
    last_error       TEXT,
    completed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_telegram_updates_status ON telegram_updates(status, locked_at);

-- 4. Telegram Messages & Domain References (Inbound + Outbound)
CREATE TABLE IF NOT EXISTS telegram_messages (
    chat_id             BIGINT NOT NULL,
    message_id          BIGINT NOT NULL,
    telegram_user_id    BIGINT, -- NULL for bot messages
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    direction           VARCHAR(10) NOT NULL, -- 'inbound', 'outbound'
    reply_to_message_id BIGINT,
    text                TEXT, -- NULL for voice, media, document etc.
    raw_payload         JSONB NOT NULL DEFAULT '{}',
    domain_references   JSONB NOT NULL DEFAULT '[]',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_tg_messages_user_time ON telegram_messages(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tg_messages_chat_reply ON telegram_messages(chat_id, reply_to_message_id);

-- 5. First-Class Workout Sessions & Proposals
CREATE TABLE IF NOT EXISTS workout_sessions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status             VARCHAR(50) NOT NULL DEFAULT 'proposed', -- 'proposed', 'accepted', 'in_progress', 'completed', 'abandoned', 'superseded'
    proposal_data      JSONB NOT NULL,
    applied_workout_id UUID REFERENCES workouts(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at         TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workout_sessions_user_status ON workout_sessions(user_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_workout_session_per_user
ON workout_sessions(user_id)
WHERE status IN ('proposed', 'in_progress', 'accepted');

CREATE TABLE IF NOT EXISTS workout_session_revisions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id         UUID NOT NULL REFERENCES workout_sessions(id) ON DELETE CASCADE,
    telegram_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
    previous_proposal  JSONB NOT NULL,
    new_proposal       JSONB NOT NULL,
    change_summary     TEXT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_session_revisions_session ON workout_session_revisions(session_id);

CREATE TABLE IF NOT EXISTS workout_ingestion_increments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workout_id          UUID NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    workout_exercise_id UUID REFERENCES workout_exercises(id) ON DELETE CASCADE,
    telegram_update_id  BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
    telegram_message_id BIGINT,
    operation           VARCHAR(50) NOT NULL, -- 'workout_created', 'exercise_added', 'sets_appended', 'corrected'
    summary             TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_workout_increments_workout ON workout_ingestion_increments(workout_id);

-- 6. Persistent Conversation State (Enforcing Telegram Private Chats)
CREATE TABLE IF NOT EXISTS conversation_states (
    user_id               UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    chat_id               BIGINT NOT NULL,
    active_session_id     UUID REFERENCES workout_sessions(id) ON DELETE SET NULL,
    focus_exercise_id     UUID REFERENCES exercises(id) ON DELETE SET NULL,
    pending_clarification JSONB,
    last_interaction_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 7. Layered User Memory (Constraints, Preferences, Equipment, Transient)
CREATE TABLE IF NOT EXISTS user_memories (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category            VARCHAR(50) NOT NULL, -- 'physical_limitation', 'equipment', 'preference', 'transient_state'
    scope               VARCHAR(30) NOT NULL DEFAULT 'permanent', -- 'permanent', 'conditional', 'transient'
    key                 VARCHAR(100) NOT NULL,
    value               JSONB NOT NULL,
    confidence          NUMERIC(3,2) NOT NULL DEFAULT 1.0,
    valid_until         TIMESTAMPTZ,
    superseded_by       UUID REFERENCES user_memories(id) ON DELETE SET NULL,
    source_update_id    BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
    source_message_id   BIGINT,
    source_text         TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_memories_lookup ON user_memories(user_id, category, scope);
CREATE INDEX IF NOT EXISTS idx_user_memories_active ON user_memories(user_id, superseded_by, valid_until);

-- 8. Historical Feedback Log (Permanent Evidence)
CREATE TABLE IF NOT EXISTS user_feedback_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workout_id     UUID REFERENCES workouts(id) ON DELETE SET NULL,
    exercise_id    UUID REFERENCES exercises(id) ON DELETE SET NULL,
    feedback_type  VARCHAR(50) NOT NULL, -- 'too_heavy', 'too_easy', 'joint_pain', 'form_fatigue', 'equipment_preference'
    raw_text       TEXT NOT NULL,
    metadata       JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_feedback_user_exercise ON user_feedback_events(user_id, exercise_id, created_at DESC);

-- 9. Audit Provenance & Update Idempotency on Workouts
ALTER TABLE workouts 
    ADD COLUMN IF NOT EXISTS source VARCHAR(50) NOT NULL DEFAULT 'manual',
    ADD COLUMN IF NOT EXISTS telegram_update_id BIGINT UNIQUE,
    ADD COLUMN IF NOT EXISTS telegram_message_id BIGINT;

-- 10. Durable Telegram Outbox / Deliveries
CREATE TABLE IF NOT EXISTS telegram_deliveries (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    update_id           BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
    chat_id             BIGINT NOT NULL,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status              VARCHAR(30) NOT NULL DEFAULT 'pending', -- 'pending', 'sending', 'sent', 'failed'
    payload             JSONB NOT NULL,
    attempts            INT NOT NULL DEFAULT 0,
    locked_at           TIMESTAMPTZ,
    locked_by           VARCHAR(100),
    last_error          TEXT,
    telegram_message_id BIGINT,
    sent_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tg_deliveries_update_id ON telegram_deliveries(update_id);

-- 11. Explicit Workout Corrections Audit
CREATE TABLE IF NOT EXISTS workout_corrections (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workout_id          UUID NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    workout_exercise_id UUID REFERENCES workout_exercises(id) ON DELETE CASCADE,
    telegram_update_id  BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
    telegram_message_id BIGINT,
    changes             JSONB NOT NULL,
    before_volume       NUMERIC(10,2) NOT NULL DEFAULT 0,
    after_volume        NUMERIC(10,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_workout_corrections_workout ON workout_corrections(workout_id);
CREATE INDEX IF NOT EXISTS idx_workout_corrections_user ON workout_corrections(user_id);

