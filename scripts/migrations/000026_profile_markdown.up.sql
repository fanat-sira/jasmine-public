ALTER TABLE users ADD COLUMN profile_markdown TEXT;
ALTER TABLE users ADD COLUMN profile_markdown_updated_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN profile_markdown_source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN profile_markdown_source_message_id BIGINT;
