CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name  TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  last_login_at INTEGER
);

CREATE TABLE sessions (
  token        TEXT PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);
CREATE INDEX sessions_expires_idx ON sessions(expires_at);
CREATE INDEX sessions_user_idx    ON sessions(user_id);

CREATE TABLE login_attempts (
  email          TEXT PRIMARY KEY COLLATE NOCASE,
  failed_count   INTEGER NOT NULL DEFAULT 0,
  last_failed_at INTEGER NOT NULL DEFAULT 0,
  locked_until   INTEGER
);

CREATE TABLE app_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
-- schema_version lives in app_meta (not PRAGMA user_version) so the
-- bump participates in the same transaction as the DDL. The runner in
-- internal/db.applyMigration writes the value as part of the same
-- transaction; migration files should not write it themselves.

CREATE TABLE feeds (
  id              INTEGER PRIMARY KEY,
  url             TEXT NOT NULL UNIQUE,
  title           TEXT NOT NULL DEFAULT '',
  site_url        TEXT NOT NULL DEFAULT '',
  category        TEXT NOT NULL DEFAULT '',
  etag            TEXT NOT NULL DEFAULT '',
  last_modified   TEXT NOT NULL DEFAULT '',
  last_fetched_at INTEGER,
  last_error      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE items (
  id           INTEGER PRIMARY KEY,
  feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  guid         TEXT NOT NULL,
  url          TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  author       TEXT NOT NULL DEFAULT '',
  content      TEXT NOT NULL DEFAULT '',
  published_at INTEGER,
  fetched_at   INTEGER NOT NULL,
  read_at      INTEGER,
  UNIQUE(feed_id, guid)
);
CREATE INDEX items_published_idx ON items(published_at DESC);
CREATE INDEX items_feed_id_idx   ON items(feed_id);

CREATE TABLE mentions (
  id          INTEGER PRIMARY KEY,
  source      TEXT NOT NULL,
  target      TEXT NOT NULL,
  status      TEXT NOT NULL CHECK(status IN ('pending','verified','rejected','removed')),
  received_at INTEGER NOT NULL,
  verified_at INTEGER,
  last_error  TEXT NOT NULL DEFAULT '',
  UNIQUE(source, target)
);
CREATE INDEX mentions_target_idx ON mentions(target, status);
