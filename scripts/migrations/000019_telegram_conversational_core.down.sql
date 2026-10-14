-- 000019_telegram_conversational_core.down.sql

DROP TABLE IF EXISTS workout_ingestion_increments;
DROP TABLE IF EXISTS workout_session_revisions;
DROP TABLE IF EXISTS workout_corrections;
DROP TABLE IF EXISTS telegram_deliveries;
DROP TABLE IF EXISTS user_feedback_events;
DROP TABLE IF EXISTS user_memories;
DROP TABLE IF EXISTS conversation_states;
DROP TABLE IF EXISTS workout_sessions;
DROP TABLE IF EXISTS telegram_messages;
DROP TABLE IF EXISTS telegram_updates;
DROP TABLE IF EXISTS telegram_users;

ALTER TABLE workouts
    DROP COLUMN IF EXISTS telegram_message_id,
    DROP COLUMN IF EXISTS telegram_update_id,
    DROP COLUMN IF EXISTS source;

-- NOTE: To safely restore NOT NULL constraints on legacy auth columns when Telegram-native users exist,
-- synthetic placeholder credentials must be populated for users where email or password_hash is NULL.
UPDATE users
SET email = 'legacy_migrated_' || id || '@jasmine.local',
    password_hash = '$2a$10$dummyhashplaceholderforrollbackonly000000000000000000'
WHERE email IS NULL OR password_hash IS NULL;

ALTER TABLE users DROP COLUMN IF EXISTS auth_provider;
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
