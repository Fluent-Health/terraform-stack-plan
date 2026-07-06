-- +goose Up
ALTER TABLE applylock_checks ADD COLUMN execution_id TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE applylock_checks DROP COLUMN execution_id;
