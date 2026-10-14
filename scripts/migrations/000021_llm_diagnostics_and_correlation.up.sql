-- Checkpoint 7: LLM Call Diagnostics, Correlation Tracing, and System Prompt Audit Revisions

-- 1. Add correlation_id columns across domain entities if not present
ALTER TABLE telegram_updates ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE telegram_deliveries ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE telegram_messages ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE user_memories ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE user_feedback_events ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE workout_session_revisions ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE workout_corrections ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE workout_ingestion_increments ADD COLUMN IF NOT EXISTS correlation_id TEXT;

CREATE INDEX IF NOT EXISTS idx_telegram_updates_correlation ON telegram_updates(correlation_id);
CREATE INDEX IF NOT EXISTS idx_telegram_deliveries_correlation ON telegram_deliveries(correlation_id);
CREATE INDEX IF NOT EXISTS idx_telegram_messages_correlation ON telegram_messages(correlation_id);

-- 2. LLM Call Diagnostics table
CREATE TABLE IF NOT EXISTS llm_call_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    correlation_id TEXT NOT NULL,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    operation TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'openrouter',
    model TEXT NOT NULL,
    latency_ms BIGINT NOT NULL,
    success BOOLEAN NOT NULL DEFAULT TRUE,
    error_message TEXT,
    retry_count INT NOT NULL DEFAULT 0,
    input_tokens INT,
    output_tokens INT,
    structured_output_valid BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_llm_call_logs_correlation ON llm_call_logs(correlation_id);
CREATE INDEX IF NOT EXISTS idx_llm_call_logs_user_id ON llm_call_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_llm_call_logs_created_at ON llm_call_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_llm_call_logs_success ON llm_call_logs(success);
CREATE INDEX IF NOT EXISTS idx_llm_call_logs_operation ON llm_call_logs(operation);

-- 3. System Prompt Version Revisions Audit
CREATE TABLE IF NOT EXISTS system_prompt_revisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prompt_key VARCHAR(64) NOT NULL,
    version INT NOT NULL,
    content TEXT NOT NULL,
    description TEXT,
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    change_summary TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_system_prompt_revisions_key ON system_prompt_revisions(prompt_key, version DESC);
