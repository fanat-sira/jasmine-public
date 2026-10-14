DROP INDEX IF EXISTS products_image_source_unique;
ALTER TABLE products DROP COLUMN IF EXISTS source_file_unique_id;
ALTER TABLE products DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE products DROP COLUMN IF EXISTS source_message_id;
ALTER TABLE products DROP COLUMN IF EXISTS source_update_id;
ALTER TABLE products DROP COLUMN IF EXISTS source_values_basis;
ALTER TABLE products DROP COLUMN IF EXISTS source_barcode;
ALTER TABLE products DROP COLUMN IF EXISTS nutrition_source;
DROP TABLE IF EXISTS telegram_media_groups;
DROP INDEX IF EXISTS workout_increments_image_source_unique;
DROP INDEX IF EXISTS body_measurements_image_source_unique;
ALTER TABLE body_measurements DROP COLUMN IF EXISTS source_file_unique_id;
ALTER TABLE body_measurements DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE body_measurements DROP COLUMN IF EXISTS source_message_id;
ALTER TABLE body_measurements DROP COLUMN IF EXISTS source_update_id;
DROP INDEX IF EXISTS meal_entries_image_source_unique;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS source_file_unique_id;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS correlation_id;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS source_item_index;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS source_message_id;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS source_update_id;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS meal_group_id;
DELETE FROM system_prompts WHERE key IN ('image_classification','food_image_analysis','nutrition_label_analysis','nutrition_ingredient_estimate','gym_machine_display_analysis','scale_analysis','equipment_image_analysis','document_image_analysis','other_image_analysis');
ALTER TABLE system_prompts DROP COLUMN IF EXISTS model;
ALTER TABLE system_prompts DROP COLUMN IF EXISTS description;
ALTER TABLE system_prompts DROP COLUMN IF EXISTS content;
ALTER TABLE system_prompts DROP COLUMN IF EXISTS version;

-- Preserve observed diary data when returning to the previous required-product schema.
DO $rollback$
DECLARE item RECORD; product UUID;
BEGIN
 FOR item IN SELECT id,user_id,observed_name,weight_g,calories,proteins,fats,carbs FROM meal_entries WHERE product_id IS NULL LOOP
  INSERT INTO products(name,calories,proteins,fats,carbs,source,owner_id)
  VALUES (LEFT(item.observed_name,255),item.calories*100/NULLIF(item.weight_g,0),item.proteins*100/NULLIF(item.weight_g,0),item.fats*100/NULLIF(item.weight_g,0),item.carbs*100/NULLIF(item.weight_g,0),'user',item.user_id)
  RETURNING id INTO product;
  UPDATE meal_entries SET product_id=product WHERE id=item.id;
 END LOOP;
END $rollback$;
ALTER TABLE meal_entries DROP CONSTRAINT IF EXISTS meal_entries_product_or_observation;
ALTER TABLE meal_entries DROP COLUMN IF EXISTS observed_name;
ALTER TABLE meal_entries ALTER COLUMN product_id SET NOT NULL;
