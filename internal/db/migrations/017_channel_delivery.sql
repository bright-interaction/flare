-- +goose Up
-- Per-channel delivery status so a silently-failing notification channel (a
-- revoked Slack webhook, a wrong email address, an unreachable endpoint) is
-- visible in the UI instead of failing forever into a slog.Warn nobody reads.
-- Written by both real alert dispatch and the manual "send test" action. This
-- is the guardrail against the estate's "nothing could page a human" incident:
-- an operator can now SEE that a channel last failed and why.
ALTER TABLE notification_channels ADD COLUMN last_attempt_at timestamptz;
ALTER TABLE notification_channels ADD COLUMN last_ok_at timestamptz;
ALTER TABLE notification_channels ADD COLUMN last_error text;

-- +goose Down
ALTER TABLE notification_channels DROP COLUMN last_attempt_at;
ALTER TABLE notification_channels DROP COLUMN last_ok_at;
ALTER TABLE notification_channels DROP COLUMN last_error;
