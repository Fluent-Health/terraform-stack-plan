-- +goose Up
ALTER TABLE gate_targets ADD COLUMN requester TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gate_targets DROP COLUMN requester;
