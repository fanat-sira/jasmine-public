-- Extra invite codes for development / testing.
-- Production: remove this migration or do not run it.
INSERT INTO invite_codes (code, expires_at)
VALUES
    ('JASMINE-DEV-001', NOW() + INTERVAL '365 days'),
    ('JASMINE-DEV-002', NOW() + INTERVAL '365 days'),
    ('JASMINE-DEV-003', NOW() + INTERVAL '365 days'),
    ('JASMINE-DEV-004', NOW() + INTERVAL '365 days'),
    ('JASMINE-DEV-005', NOW() + INTERVAL '365 days')
ON CONFLICT DO NOTHING;
