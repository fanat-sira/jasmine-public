-- Enable pg_trgm for fast ILIKE / similarity search on product names.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ── products ─────────────────────────────────────────────────────────────────
CREATE TABLE products (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name       VARCHAR(255) NOT NULL,
    barcode    VARCHAR(100) UNIQUE,
    calories   DECIMAL(8,2) NOT NULL DEFAULT 0,
    proteins   DECIMAL(8,2) NOT NULL DEFAULT 0,
    fats       DECIMAL(8,2) NOT NULL DEFAULT 0,
    carbs      DECIMAL(8,2) NOT NULL DEFAULT 0,
    source     VARCHAR(20)  NOT NULL DEFAULT 'user' CHECK (source IN ('usda','off','user')),
    owner_id   UUID         REFERENCES users(id) ON DELETE SET NULL,
    verified   BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Trigram index enables fast partial-match search (ILIKE '%query%').
CREATE INDEX idx_products_name_trgm ON products USING GIN (name gin_trgm_ops);
-- B-tree for barcode lookups.
CREATE INDEX idx_products_barcode   ON products (barcode) WHERE barcode IS NOT NULL;
-- Partial index for listing a user's own products.
CREATE INDEX idx_products_owner     ON products (owner_id) WHERE owner_id IS NOT NULL;

-- Product catalogue is populated via API/import flows (no hardcoded seed rows).
