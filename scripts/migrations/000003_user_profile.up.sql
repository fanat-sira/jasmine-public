ALTER TABLE users
    ADD COLUMN weight_kg        NUMERIC(5,2) DEFAULT NULL,
    ADD COLUMN height_cm        SMALLINT     DEFAULT NULL,
    ADD COLUMN target_calories  SMALLINT     DEFAULT NULL;
