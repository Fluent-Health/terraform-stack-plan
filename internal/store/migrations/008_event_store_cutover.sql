-- +goose Up
-- Greenfield cutover to the event log: gate_runs is subsumed (classified-ness is
-- now the folded gate variant). gate_targets becomes a derived projection; clear
-- stale rows so it starts empty, consistent with the empty event log (in-flight
-- PRs re-plan to re-establish events).
DELETE FROM gate_targets;
DROP TABLE gate_runs;

-- +goose Down
CREATE TABLE gate_runs (
    pr            INTEGER NOT NULL,
    environment   TEXT NOT NULL,
    classified_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (pr, environment)
);
