-- Review of the Outlook crash-safety round.

-- A connector's own state across app restarts (source.StateKeeper): the
-- Outlook breaker's losses and stop survive a restart of the app.
CREATE TABLE source_state (
    source_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (source_id, name)
) STRICT;

-- Messages whose detail read made the source fail as a whole (Outlook
-- raised an exception reading them): set aside at that revision, never
-- read again automatically (newest first, they would be read first on
-- every run); a new revision or the user's action (Syncer.RetryDetails)
-- clears them.
CREATE TABLE detail_quarantine (
    message_id     INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    revision       INTEGER NOT NULL,
    code           TEXT NOT NULL,
    quarantined_at TEXT NOT NULL
) STRICT;

-- A paid decision stays valid for its message until the message's own
-- revision changes: the revision it was decided at. Rows written before
-- are taken as decided at the message's current revision (paid answers
-- are kept, never re-billed by this migration).
ALTER TABLE triage_decisions ADD COLUMN message_revision INTEGER NOT NULL DEFAULT -1;
UPDATE triage_decisions SET message_revision = COALESCE((SELECT revision FROM messages WHERE id = message_id), -1);

-- The version of the text a vector was computed from (the cleaning rules'
-- data digest): a change of the cleaning rechecks every vector's text
-- (re-embedded only where the text changed). '' for rows written before.
ALTER TABLE embeddings ADD COLUMN text_version TEXT NOT NULL DEFAULT '';
