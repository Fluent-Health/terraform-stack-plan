-- +goose Up
-- Per-stack output (logs now; plan/verify kinds later): a pointer to the
-- offloaded full object and a tail excerpt for instant render.
CREATE TABLE stack_outputs (
    execution_id TEXT NOT NULL,
    stack_path   TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'log',
    pointer      TEXT,
    excerpt      TEXT,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (execution_id, stack_path, kind)
);

-- +goose Down
DROP TABLE stack_outputs;
