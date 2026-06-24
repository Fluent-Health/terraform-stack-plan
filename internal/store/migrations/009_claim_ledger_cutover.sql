-- +goose Up
-- Claim ledger cut over to the env:<env> event log: apply_claims is now a derived
-- projection. Clear stale rows so it starts empty, consistent with the empty event
-- streams (in-flight applies re-claim on their next greenlight; deploy when no prod
-- apply is in flight).
DELETE FROM apply_claims;

-- +goose Down
-- (no-op: apply_claims schema is unchanged; rows are transient)
SELECT 1;
