-- +goose Up
ALTER TABLE executions ADD COLUMN progress_label TEXT;
ALTER TABLE executions ADD COLUMN progress_pct INTEGER;

-- +goose Down
ALTER TABLE executions DROP COLUMN progress_label;
ALTER TABLE executions DROP COLUMN progress_pct;
