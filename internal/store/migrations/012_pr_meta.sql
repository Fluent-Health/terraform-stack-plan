-- +goose Up
CREATE TABLE pr_meta (
  repo         TEXT    NOT NULL,
  pr           INTEGER NOT NULL,
  title        TEXT    NOT NULL DEFAULT '',
  body         TEXT    NOT NULL DEFAULT '',
  author_login TEXT    NOT NULL DEFAULT '',
  head_ref     TEXT    NOT NULL DEFAULT '',
  url          TEXT    NOT NULL DEFAULT '',
  auto_merge   INTEGER NOT NULL DEFAULT 0,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (repo, pr)
);
-- +goose Down
DROP TABLE pr_meta;
