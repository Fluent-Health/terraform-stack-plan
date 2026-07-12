-- +goose Up
ALTER TABLE executions ADD COLUMN change_reasons TEXT;

-- +goose Down
ALTER TABLE executions DROP COLUMN change_reasons;
