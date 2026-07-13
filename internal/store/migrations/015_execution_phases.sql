-- +goose Up
CREATE TABLE execution_phases (
    execution_id TEXT NOT NULL,
    phase        TEXT NOT NULL,
    label        TEXT,
    progress_pct INTEGER,
    at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_execution_phases_exec ON execution_phases(execution_id, at);

-- +goose Down
DROP TABLE execution_phases;
