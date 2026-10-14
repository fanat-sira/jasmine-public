CREATE TABLE assistant_preferences (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    version BIGINT NOT NULL DEFAULT 0,
    profile JSONB NOT NULL DEFAULT '{"settings":{},"custom_rules":[],"custom_instructions":"","personality":""}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (jsonb_typeof(profile) = 'object' AND octet_length(profile::text) <= 65536)
);
CREATE TABLE assistant_preference_revisions (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    version BIGINT NOT NULL,
    previous_profile JSONB NOT NULL,
    profile JSONB NOT NULL,
    source_update_id BIGINT NOT NULL,
    source_message_id BIGINT NOT NULL,
    source_filename TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    confirmed BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, version),
    UNIQUE (user_id, source_update_id)
);
CREATE TABLE assistant_preference_proposals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id BIGINT NOT NULL,
    source_update_id BIGINT NOT NULL,
    source_message_id BIGINT NOT NULL,
    source_filename TEXT NOT NULL,
    changes JSONB NOT NULL,
    base_version BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','cancelled')),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, source_update_id),
    CHECK (octet_length(changes::text) <= 65536)
);
ALTER TABLE assistant_preference_revisions ADD COLUMN source_proposal_id UUID REFERENCES assistant_preference_proposals(id);
CREATE INDEX assistant_preference_proposals_owner ON assistant_preference_proposals(user_id,chat_id,status);

-- Carry recognized historical style facts into the always-loaded profile.
-- Unknown predicates and arbitrary old prose are deliberately not promoted.
INSERT INTO assistant_preferences(user_id,profile)
SELECT user_id,jsonb_build_object('settings',jsonb_object_agg(predicate,setting),'custom_rules','[]'::jsonb,'custom_instructions','','personality','')
FROM (
 SELECT DISTINCT ON (user_id,predicate) user_id,predicate,
 CASE WHEN predicate='final_sentence_period' AND value='false'::jsonb THEN 'disabled'
      WHEN predicate='final_sentence_period' AND value='true'::jsonb THEN 'allowed'
      ELSE value #>> '{}' END AS setting
 FROM user_memories
 WHERE category='personal_fact' AND namespace='assistant_preferences' AND subject='self' AND superseded_by IS NULL
   AND (valid_until IS NULL OR valid_until>NOW())
   AND ((predicate='final_sentence_period' AND value IN ('false'::jsonb,'true'::jsonb))
     OR (predicate='form_of_address' AND value #>> '{}' IN ('formal','informal'))
     OR (predicate='language' AND value #>> '{}' IN ('ru','en','auto'))
     OR (predicate='tone' AND value #>> '{}' IN ('neutral','friendly','playful','professional'))
     OR (predicate='verbosity' AND value #>> '{}' IN ('concise','balanced','detailed'))
     OR (predicate='humor' AND value #>> '{}' IN ('none','occasional','frequent'))
     OR (predicate='emoji' AND value #>> '{}' IN ('allowed','disabled'))
     OR (predicate='formatting' AND value #>> '{}' IN ('structured','conversational'))
     OR (predicate='grammatical_address' AND value #>> '{}' IN ('masculine','feminine','neutral')))
 ORDER BY user_id,predicate,created_at DESC,id DESC
) active_settings GROUP BY user_id
ON CONFLICT(user_id) DO NOTHING;
