-- Keep every Telegram/domain user and their workout, nutrition and profile data.
-- Refuse ambiguous historical ownership rather than silently sharing profiles.
DO $$
BEGIN
    IF EXISTS (SELECT user_id FROM telegram_users GROUP BY user_id HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION 'Multiple Telegram IDs share one domain user; review ownership before Telegram-only migration';
    END IF;
END $$;

ALTER TABLE telegram_users ADD CONSTRAINT telegram_users_user_id_unique UNIQUE (user_id);

-- These records belong only to the retired HTTP account application.
DROP TABLE refresh_tokens;
DROP TABLE invite_codes;
DROP TABLE ai_generated_workouts;
ALTER TABLE users
    DROP COLUMN email,
    DROP COLUMN password_hash,
    DROP COLUMN auth_provider,
    DROP COLUMN is_admin;
