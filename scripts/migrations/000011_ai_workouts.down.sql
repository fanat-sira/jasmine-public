-- 000011_ai_workouts.down.sql

DROP TABLE IF EXISTS ai_generated_workouts;
ALTER TABLE users DROP COLUMN IF EXISTS goal;
