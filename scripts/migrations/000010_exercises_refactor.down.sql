-- 000010_exercises_refactor.down.sql

ALTER TABLE workouts ADD COLUMN IF NOT EXISTS exercises JSONB NOT NULL DEFAULT '[]';
DROP TABLE IF EXISTS workout_exercises;
DROP TABLE IF EXISTS exercises;
