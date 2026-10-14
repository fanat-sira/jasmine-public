DROP INDEX IF EXISTS idx_user_custom_measurements_user_created;
DROP TABLE IF EXISTS user_custom_measurements;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_default_rest_sec_check,
    DROP CONSTRAINT IF EXISTS users_units_check,
    DROP COLUMN IF EXISTS default_rest_sec,
    DROP COLUMN IF EXISTS units,
    DROP COLUMN IF EXISTS avatar_data,
    DROP COLUMN IF EXISTS age;
