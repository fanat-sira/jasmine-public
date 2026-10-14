CREATE TABLE personal_documents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_update_id BIGINT UNIQUE REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  source_message_id BIGINT NOT NULL,
  telegram_file_id TEXT,
  telegram_file_unique_id TEXT,
  filename TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  format TEXT NOT NULL CHECK (format IN ('markdown','text','json')),
  content_sha256 TEXT NOT NULL,
  content_text TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX personal_documents_user_created ON personal_documents(user_id,created_at DESC);
ALTER TABLE user_memories ADD COLUMN source_document_id UUID REFERENCES personal_documents(id) ON DELETE SET NULL;
ALTER TABLE personal_fact_events ADD COLUMN source_document_id UUID REFERENCES personal_documents(id) ON DELETE SET NULL;
CREATE INDEX user_memories_personal_document ON user_memories(user_id,source_document_id) WHERE category='personal_fact';
