-- 000018_remove_legacy_seed_catalog.up.sql
-- Removes legacy hardcoded catalog rows from earlier migrations.
-- After this migration, products/exercises are expected to be managed via API/import flows.

DELETE FROM exercises e
WHERE e.is_default = TRUE
  AND NOT EXISTS (
      SELECT 1
      FROM workout_exercises we
      WHERE we.exercise_id = e.id
  );

DELETE FROM products p
WHERE p.source = 'usda'
  AND p.owner_id IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM meal_entries me
      WHERE me.product_id = p.id
  );
