DROP TABLE IF EXISTS personal_fact_events;
DROP TABLE IF EXISTS meal_entry_corrections;
DROP TABLE IF EXISTS meal_observation_corrections;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS deleted_at;
DROP TABLE IF EXISTS meal_conversation_context;
DROP TABLE IF EXISTS meal_observations;
DROP INDEX IF EXISTS body_measurements_profile_source_unique;
ALTER TABLE body_measurements DROP COLUMN IF EXISTS source_profile_fingerprint;
DROP INDEX IF EXISTS user_memories_personal_lookup;
DROP INDEX IF EXISTS user_memories_fact_update_idempotency;
DROP INDEX IF EXISTS user_memories_current_fact_identity;
ALTER TABLE user_memories DROP COLUMN IF EXISTS operation, DROP COLUMN IF EXISTS valid_from,
  DROP COLUMN IF EXISTS source_id, DROP COLUMN IF EXISTS source_type, DROP COLUMN IF EXISTS predicate,
  DROP COLUMN IF EXISTS subject, DROP COLUMN IF EXISTS namespace;
DROP TABLE IF EXISTS telegram_media_group_items;
DROP INDEX IF EXISTS telegram_media_groups_ready_idx;
ALTER TABLE telegram_media_groups DROP CONSTRAINT IF EXISTS telegram_media_groups_status_check;
ALTER TABLE telegram_media_groups DROP COLUMN IF EXISTS processed_at, DROP COLUMN IF EXISTS locked_at,
  DROP COLUMN IF EXISTS attempts, DROP COLUMN IF EXISTS status, DROP COLUMN IF EXISTS last_seen_at,
  DROP COLUMN IF EXISTS first_message_id, DROP COLUMN IF EXISTS caption, DROP COLUMN IF EXISTS chat_id;
