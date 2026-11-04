-- Phase 2b: the semantic index.

-- Every write of a message (store.writer, the one write path) increments
-- its revision: the exact change marker derived data compares against
-- (updated_at has one-second precision).
ALTER TABLE messages ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;

-- One vector per message, in the vector
-- space of the embedder that wrote it (provider/model/dimensions/task
-- templates): search compares only vectors of the current space. A failed
-- run never removes a vector; a removed message takes its vector along.
CREATE TABLE embeddings (
    message_id         INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    space              TEXT NOT NULL,
    -- digest of the exact input embedded (template + cleaned, cut text):
    -- an unchanged text is never embedded again
    text_hash          TEXT NOT NULL,
    -- messages.revision when embedded or last checked (the cheap change test)
    message_revision   INTEGER NOT NULL,
    dims               INTEGER NOT NULL,
    -- int8 values (vector.Quantized, one byte per dimension) and their scale
    vector             BLOB NOT NULL,
    scale              REAL NOT NULL,
    -- write order (meta embedding_seq: never reused, also after deletes):
    -- search loads only the rows written since its last load
    seq                INTEGER NOT NULL,
    embedded_at        TEXT NOT NULL
) STRICT;

CREATE INDEX embeddings_space_seq ON embeddings(space, seq);

-- Failed embedding attempts per message and revision: a message that
-- keeps failing (an input that crashes the runtime) is quarantined after
-- index.quarantine_after attempts until it changes.
CREATE TABLE index_failures (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    revision   INTEGER NOT NULL,
    failures   INTEGER NOT NULL,
    error      TEXT NOT NULL,
    failed_at  TEXT NOT NULL
) STRICT;

CREATE TABLE index_runs (
    id          INTEGER PRIMARY KEY,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    space       TEXT NOT NULL,
    report      TEXT NOT NULL DEFAULT '{}',
    error       TEXT NOT NULL DEFAULT ''
) STRICT;
