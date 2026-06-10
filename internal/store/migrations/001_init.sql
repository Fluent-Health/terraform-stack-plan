-- +goose Up
CREATE TABLE executions (
    id              TEXT PRIMARY KEY,
    repo            TEXT NOT NULL,
    sha             TEXT NOT NULL,
    pr              INTEGER,
    environment     TEXT,
    check_run_id    INTEGER,
    rev             INTEGER NOT NULL DEFAULT 0,
    report_markdown TEXT,
    log_url         TEXT,
    status          TEXT DEFAULT 'in_progress',
    status_context  TEXT,
    phase           TEXT,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE stacks (
    execution_id TEXT NOT NULL,
    stack_path   TEXT NOT NULL,
    project      TEXT,
    status       TEXT DEFAULT 'pending',
    detail       TEXT,
    PRIMARY KEY (execution_id, stack_path),
    FOREIGN KEY (execution_id) REFERENCES executions(id)
);

CREATE TABLE edges (
    execution_id TEXT NOT NULL,
    from_stack   TEXT NOT NULL,
    to_stack     TEXT NOT NULL,
    PRIMARY KEY (execution_id, from_stack, to_stack),
    FOREIGN KEY (execution_id) REFERENCES executions(id)
);

-- gate_targets generalises the single implicit IAM/project gate into
-- (class, target) pairs. No foreign key to executions: async verdict logic
-- (approval events, reconcile loop) may process gates whose execution row has
-- been pruned.
CREATE TABLE gate_targets (
    pr          INTEGER NOT NULL,
    environment TEXT NOT NULL,
    class       TEXT NOT NULL,
    target      TEXT NOT NULL,
    grant_name  TEXT,
    state       TEXT,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (pr, environment, class, target)
);

-- gate_runs records that a (pr, environment) was classified by a plan, even when
-- the plan was clean (zero gate_targets) — lets the apply gate distinguish a
-- clean PR (proceed) from a never-planned one (fail closed).
CREATE TABLE gate_runs (
    pr            INTEGER NOT NULL,
    environment   TEXT NOT NULL,
    classified_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (pr, environment)
);

CREATE INDEX idx_executions_pr_env ON executions(pr, environment);
CREATE INDEX idx_executions_created ON executions(created_at);

-- +goose Down
DROP INDEX idx_executions_created;
DROP INDEX idx_executions_pr_env;
DROP TABLE gate_runs;
DROP TABLE gate_targets;
DROP TABLE edges;
DROP TABLE stacks;
DROP TABLE executions;
