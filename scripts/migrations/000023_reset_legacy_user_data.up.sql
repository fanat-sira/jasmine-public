-- One-time reset for the Telegram-first relaunch. Keep the exercise catalogue,
-- prompt definitions, schema and migration history. The catalog importer runs
-- after migrations and fills any missing exercise rows before the bot starts.
--
-- One TRUNCATE makes the data reset atomic and blocks concurrent writes while
-- it runs. CASCADE clears all user-owned tables; the other names include data
-- without an owner FK or with ON DELETE SET NULL. Neither exercises nor
-- system_prompts references these tables, so both stay intact.
TRUNCATE TABLE users, telegram_updates, products, invite_codes, system_prompt_revisions
    RESTART IDENTITY CASCADE;

-- Coach context reads the latest 50 messages by chat and timestamp.
CREATE INDEX IF NOT EXISTS idx_tg_messages_chat_time
    ON telegram_messages(chat_id, created_at DESC, message_id DESC);
