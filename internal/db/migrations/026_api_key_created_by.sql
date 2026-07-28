-- +goose Up
-- Record WHO minted an API key, so a password reset can revoke the keys that
-- user created.
--
-- A reset already bumps sessions_valid_from, which kicks an attacker out of
-- every session. It did nothing about API keys, so an attacker who used their
-- access to mint one kept a credential that outlives the reset entirely: the
-- reset is the moment the victim believes they have taken the account back.
--
-- Revoking ALL of the org's keys on any member's reset is not the answer. Keys
-- are org-scoped and drive CI, so one person resetting their password would
-- break every pipeline in the workspace. The keys that matter are the ones the
-- compromised account created, and until now nothing recorded that.
--
-- NULLable on purpose. Keys that predate this migration have no known creator
-- and are NOT revoked by a reset; inventing an owner for them would be worse
-- than admitting we do not know. The API surfaces the creator so an operator can
-- see which keys are in that state and rotate them deliberately.
--
-- ON DELETE SET NULL rather than CASCADE: removing a user must not silently
-- delete the CI keys they happened to create. Losing the attribution is
-- acceptable; losing a working deploy credential without warning is not.
ALTER TABLE api_keys
    ADD COLUMN created_by_user_id TEXT
    REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS api_keys_created_by_idx
    ON api_keys (created_by_user_id)
    WHERE created_by_user_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS api_keys_created_by_idx;
ALTER TABLE api_keys DROP COLUMN IF EXISTS created_by_user_id;
