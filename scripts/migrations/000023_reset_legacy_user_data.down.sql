-- Deleted user data cannot be restored by a migration. A rollback only removes
-- the index introduced by the up migration.
DROP INDEX IF EXISTS idx_tg_messages_chat_time;
