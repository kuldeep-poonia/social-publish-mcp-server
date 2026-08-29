CREATE TABLE IF NOT EXISTS brand_personas (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    brand_name VARCHAR(255) NOT NULL,
    tone VARCHAR(100) NOT NULL DEFAULT 'authoritative_inspirational',
    visual_style VARCHAR(255) NOT NULL DEFAULT 'modern_cinematic_minimalist',
    color_palette VARCHAR(255) NOT NULL DEFAULT '#0F172A, #38BDF8, #818CF8',
    voice_guidelines TEXT NOT NULL DEFAULT '',
    forbidden_words TEXT[] NOT NULL DEFAULT '{}',
    target_audience VARCHAR(255) NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_brand_personas_user_default ON brand_personas (user_id) WHERE is_default = TRUE;
CREATE INDEX IF NOT EXISTS idx_brand_personas_user_id ON brand_personas (user_id);
