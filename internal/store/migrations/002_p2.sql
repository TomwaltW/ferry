-- Ferry P2: held messages and long-output files.

ALTER TABLE outbox ADD COLUMN held_at INTEGER NOT NULL DEFAULT 0;

CREATE TABLE long_outputs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  endpoint   TEXT NOT NULL,
  session_id TEXT NOT NULL,
  path       TEXT NOT NULL,
  chars      INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX long_outputs_endpoint ON long_outputs(endpoint, id);
