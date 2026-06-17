-- +goose Up
-- Per-stack operation counts (JSON {add,change,destroy,replace,move,import,forget}),
-- used by the blast-radius bar and op-count summaries. Backfilled at finalize.
ALTER TABLE stacks ADD COLUMN counts TEXT;

-- +goose Down
ALTER TABLE stacks DROP COLUMN counts;
