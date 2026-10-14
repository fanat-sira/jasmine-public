DROP INDEX IF EXISTS user_memories_personal_document;
ALTER TABLE personal_fact_events DROP COLUMN IF EXISTS source_document_id;
ALTER TABLE user_memories DROP COLUMN IF EXISTS source_document_id;
DROP TABLE IF EXISTS personal_documents;
