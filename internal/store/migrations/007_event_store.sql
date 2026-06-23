-- +goose Up
CREATE TABLE events (
    stream_id   TEXT    NOT NULL,
    version     INTEGER NOT NULL,
    type        TEXT    NOT NULL,
    data        BLOB    NOT NULL,
    occurred_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stream_id, version)
);

CREATE TABLE snapshots (
    stream_id  TEXT    PRIMARY KEY,
    version    INTEGER NOT NULL,
    state      BLOB    NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE snapshots;
DROP TABLE events;
