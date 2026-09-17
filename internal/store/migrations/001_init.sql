-- Ferry P1 initial schema. Times are Unix milliseconds.
-- Connection parameters: journal_mode=WAL, busy_timeout=5000, foreign_keys=ON.

CREATE TABLE schema_version (version INTEGER NOT NULL);

CREATE TABLE sessions (
  id               TEXT PRIMARY KEY,
  driver           TEXT NOT NULL,
  agent            TEXT NOT NULL,
  cwd              TEXT NOT NULL,
  agent_session_id TEXT NOT NULL DEFAULT '',
  origin           TEXT NOT NULL,
  state            TEXT NOT NULL,
  owner            TEXT NOT NULL,          -- EndpointID.String()
  created_at       INTEGER NOT NULL,
  last_active      INTEGER NOT NULL
);

CREATE TABLE bindings (
  endpoint   TEXT NOT NULL,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  slot       INTEGER NOT NULL,
  foreground INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (endpoint, session_id),
  UNIQUE (endpoint, slot)
);

CREATE TABLE dedupe (
  endpoint   TEXT NOT NULL,
  message_id TEXT NOT NULL,
  seen_at    INTEGER NOT NULL,
  PRIMARY KEY (endpoint, message_id)
);

CREATE TABLE outbox (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  endpoint   TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL,
  body       TEXT NOT NULL,
  file_path  TEXT NOT NULL DEFAULT '',
  dedupe_key TEXT NOT NULL,
  urgent     INTEGER NOT NULL,
  state      TEXT NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX outbox_endpoint_state ON outbox(endpoint, state);

CREATE TABLE windows (
  endpoint  TEXT PRIMARY KEY,
  reply_ctx TEXT NOT NULL,
  opened_at INTEGER NOT NULL,
  used      INTEGER NOT NULL
);

CREATE TABLE approvals (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  request    TEXT NOT NULL,              -- ApprovalRequest 的 JSON
  decision   TEXT NOT NULL DEFAULT '',
  decided_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  decided_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE acl (
  endpoint  TEXT NOT NULL,
  sender_id TEXT NOT NULL,
  role      TEXT NOT NULL,
  PRIMARY KEY (endpoint, sender_id)
);

CREATE TABLE pairing (
  code       TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL,
  sender_id  TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE audit (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  endpoint   TEXT NOT NULL DEFAULT '',
  sender_id  TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE kv (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
