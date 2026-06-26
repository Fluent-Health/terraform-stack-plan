-- +goose Up
ALTER TABLE executions ADD COLUMN superseded_by TEXT;

-- +goose Down
ALTER TABLE executions DROP COLUMN superseded_by;
