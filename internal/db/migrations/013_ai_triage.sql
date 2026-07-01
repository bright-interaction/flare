-- +goose Up
-- Per-org BYOAI config for sovereign AI issue triage. The tenant points Flare
-- at their own OpenAI-compatible or Anthropic-Messages endpoint (OpenAI, a
-- self-hosted vLLM/Ollama, an EU provider, ...), so inference stays where they
-- control it. api_key is admin-set and never returned by the API. PII in the
-- issue context is scrubbed before it is sent to the model.
CREATE TABLE ai_config (
    org_id     TEXT PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    base_url   TEXT NOT NULL,
    api_key    TEXT NOT NULL,
    model      TEXT NOT NULL,
    format     TEXT NOT NULL DEFAULT 'openai',  -- openai | anthropic
    enabled    BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE issues ADD COLUMN ai_triage TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN ai_triaged_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE issues DROP COLUMN IF EXISTS ai_triaged_at;
ALTER TABLE issues DROP COLUMN IF EXISTS ai_triage;
DROP TABLE IF EXISTS ai_config;
