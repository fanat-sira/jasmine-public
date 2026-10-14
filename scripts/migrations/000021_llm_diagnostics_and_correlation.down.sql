DROP TABLE IF EXISTS system_prompt_revisions;
DROP TABLE IF EXISTS llm_call_logs;

ALTER TABLE workout_ingestion_increments DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE workout_corrections DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE workout_session_revisions DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE user_feedback_events DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE user_memories DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE telegram_messages DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE telegram_deliveries DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE telegram_updates DROP COLUMN IF EXISTS correlation_id;
