-- +goose Up
CREATE TABLE apply_claims (
    environment  TEXT    NOT NULL,
    stack_path   TEXT    NOT NULL,
    owner_pr     INTEGER NOT NULL,
    execution_id TEXT,
    expires_at   DATETIME NOT NULL,
    PRIMARY KEY (environment, stack_path)
);
CREATE TABLE applylock_checks (
    environment  TEXT    NOT NULL,
    head_sha     TEXT    NOT NULL,
    check_run_id INTEGER NOT NULL,
    pr           INTEGER NOT NULL,
    repo         TEXT    NOT NULL,
    stacks       TEXT    NOT NULL,
    state        TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (environment, head_sha)
);
-- +goose Down
DROP TABLE applylock_checks;
DROP TABLE apply_claims;
