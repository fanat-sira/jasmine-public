-- Media groups are durable logical turns. Previously claimed groups were already answered
-- as unsupported; keep them closed instead of replaying old albums on deployment.
ALTER TABLE telegram_media_groups
  ADD COLUMN chat_id BIGINT,
  ADD COLUMN caption TEXT NOT NULL DEFAULT '',
  ADD COLUMN first_message_id BIGINT,
  ADD COLUMN last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN status TEXT NOT NULL DEFAULT 'processed',
  ADD COLUMN attempts INT NOT NULL DEFAULT 0,
  ADD COLUMN locked_at TIMESTAMPTZ,
  ADD COLUMN processed_at TIMESTAMPTZ;
ALTER TABLE telegram_media_groups ADD CONSTRAINT telegram_media_groups_status_check
  CHECK (status IN ('collecting','processing','processed','failed'));
CREATE INDEX telegram_media_groups_ready_idx ON telegram_media_groups(status,last_seen_at);
CREATE TABLE telegram_media_group_items (
  user_id UUID NOT NULL,
  media_group_id TEXT NOT NULL,
  update_id BIGINT NOT NULL UNIQUE REFERENCES telegram_updates(update_id) ON DELETE CASCADE,
  message_id BIGINT NOT NULL,
  file_id TEXT NOT NULL,
  file_unique_id TEXT NOT NULL,
  caption TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY(user_id,media_group_id,message_id),
  FOREIGN KEY(user_id,media_group_id) REFERENCES telegram_media_groups(user_id,media_group_id) ON DELETE CASCADE
);

-- Personal knowledge evolves user_memories; Coach memory rows keep their old shape.
ALTER TABLE user_memories
  ADD COLUMN namespace TEXT,
  ADD COLUMN subject TEXT NOT NULL DEFAULT 'self',
  ADD COLUMN predicate TEXT,
  ADD COLUMN source_type TEXT NOT NULL DEFAULT 'legacy',
  ADD COLUMN source_id TEXT,
  ADD COLUMN valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN operation TEXT NOT NULL DEFAULT 'replace';
CREATE UNIQUE INDEX user_memories_current_fact_identity ON user_memories(user_id,namespace,subject,predicate)
  WHERE category='personal_fact' AND superseded_by IS NULL AND valid_until IS NULL;
CREATE UNIQUE INDEX user_memories_fact_update_idempotency ON user_memories(user_id,source_update_id,namespace,subject,predicate)
  WHERE category='personal_fact' AND source_update_id IS NOT NULL;
CREATE INDEX user_memories_personal_lookup ON user_memories(user_id,namespace,subject,created_at DESC)
  WHERE category='personal_fact';

CREATE TABLE personal_fact_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  namespace TEXT NOT NULL,
  subject TEXT NOT NULL,
  predicate TEXT NOT NULL,
  operation TEXT NOT NULL,
  previous_fact_id UUID REFERENCES user_memories(id) ON DELETE SET NULL,
  new_fact_id UUID REFERENCES user_memories(id) ON DELETE SET NULL,
  source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  source_message_id BIGINT,
  correlation_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX personal_fact_events_user_time ON personal_fact_events(user_id,created_at DESC);

ALTER TABLE body_measurements ADD COLUMN source_profile_fingerprint TEXT;
CREATE UNIQUE INDEX body_measurements_profile_source_unique ON body_measurements(user_id,source_profile_fingerprint)
  WHERE source_profile_fingerprint IS NOT NULL;

-- Nutrition observations can be discussed before a user asks to log them.
CREATE TABLE meal_observations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  logical_turn_id TEXT NOT NULL,
  source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  source_message_id BIGINT NOT NULL,
  source_file_unique_id TEXT,
  image_index INT NOT NULL DEFAULT 0,
  item_index INT NOT NULL,
  item JSONB NOT NULL,
  meal_type TEXT NOT NULL,
  eaten_at TIMESTAMPTZ NOT NULL,
  persisted_entry_id UUID REFERENCES meal_entries(id) ON DELETE SET NULL,
  superseded_source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id,logical_turn_id,image_index,item_index)
);
CREATE INDEX meal_observations_user_date ON meal_observations(user_id,eaten_at DESC);

CREATE TABLE meal_conversation_context (
  user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  meal_group_id UUID,
  logical_turn_id TEXT,
  eaten_at TIMESTAMPTZ NOT NULL,
  meal_type TEXT NOT NULL DEFAULT 'snack',
  source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  active_until TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE meal_entries ADD COLUMN deleted_at TIMESTAMPTZ;
CREATE TABLE meal_observation_corrections (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  observation_id UUID NOT NULL REFERENCES meal_observations(id) ON DELETE CASCADE,
  operation TEXT NOT NULL,
  before_value JSONB NOT NULL,
  after_value JSONB NOT NULL,
  source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  source_message_id BIGINT,
  correlation_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id,observation_id,source_update_id)
);
CREATE TABLE meal_entry_corrections (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  meal_entry_id UUID NOT NULL REFERENCES meal_entries(id) ON DELETE CASCADE,
  operation TEXT NOT NULL,
  before_value JSONB NOT NULL,
  after_value JSONB NOT NULL,
  source_update_id BIGINT REFERENCES telegram_updates(update_id) ON DELETE SET NULL,
  source_message_id BIGINT,
  correlation_id TEXT,
  source_text TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id,meal_entry_id,source_update_id)
);
