-- Layer 0: the structural evidence of every message (internal/evidence).
-- A schema is the ordered list of signals with their effective versions
-- (version + the settings they read); a message's values are one JSON
-- array aligned with the schema it was computed under, so a signal whose
-- version changes is recomputed alone and an unchanged one is reused.
CREATE TABLE evidence_schemas (
    id      INTEGER PRIMARY KEY,
    signals TEXT NOT NULL UNIQUE -- JSON [[name, key], ...]
) STRICT;

CREATE TABLE evidence (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    revision   INTEGER NOT NULL, -- messages.revision the values were computed at
    schema_id  INTEGER NOT NULL REFERENCES evidence_schemas(id),
    vals       TEXT NOT NULL      -- JSON array aligned with the schema
) STRICT;

CREATE INDEX evidence_schema ON evidence(schema_id);

-- Evidence runs; inputs is the fingerprint of everything the run read
-- (visible mail, verdicts, vectors, settings, schema): the stage is done
-- when the last succeeded run read the current inputs.
CREATE TABLE evidence_runs (
    id          INTEGER PRIMARY KEY,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    inputs      TEXT NOT NULL DEFAULT '',
    report      TEXT NOT NULL DEFAULT '{}',
    error       TEXT NOT NULL DEFAULT ''
) STRICT;
