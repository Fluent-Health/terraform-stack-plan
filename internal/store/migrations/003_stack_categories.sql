-- +goose Up
-- Per-stack matched classification categories (JSON array of {name,icon}), used
-- for the group-level DAG's category badges. Backfilled at finalize.
ALTER TABLE stacks ADD COLUMN categories TEXT;

-- +goose Down
ALTER TABLE stacks DROP COLUMN categories;
