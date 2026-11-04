-- Phase L0: explicit mailbox selection, owner identities reported by the
-- connector (never inferred from Sent mail), connector capabilities,
-- incremental scope.

-- What each source's data holds (source.Capabilities as JSON): the layers
-- above ask instead of assuming Outlook.
ALTER TABLE sources ADD COLUMN capabilities TEXT NOT NULL DEFAULT '{}';

-- The owner of a store now comes from the mailbox list below.
ALTER TABLE folders DROP COLUMN store_owner;
UPDATE folders SET store_kind = CASE store_kind
    WHEN 'additional' THEN 'shared' WHEN 'file' THEN 'other' WHEN '' THEN 'other' ELSE store_kind END;

-- Every mailbox store the last discovery of a source saw, picked or not
-- (source.Mailbox): the picking screen and the learning status read it.
CREATE TABLE mailboxes (
    source_id     TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    store_id      TEXT NOT NULL,
    key           TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL,
    is_default    INTEGER NOT NULL,
    cached        INTEGER NOT NULL,
    folders       INTEGER NOT NULL,
    items         INTEGER NOT NULL,
    counted_at    TEXT NOT NULL,
    error         TEXT NOT NULL,
    discovered_at TEXT NOT NULL,
    PRIMARY KEY (source_id, store_id)
) STRICT;

-- Identities are now reported (connector) or declared (user), in order
-- (position 0 = primary address); the Sent-inferred ones are gone.
DELETE FROM owner_identities;
ALTER TABLE owner_identities ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

-- Incremental scope: a verdict is recomputed only when its message
-- changed (revision) or its store's identity changed (digest).
DELETE FROM scope_verdicts;
ALTER TABLE scope_verdicts ADD COLUMN revision INTEGER NOT NULL DEFAULT -1;
ALTER TABLE scope_verdicts ADD COLUMN identity TEXT NOT NULL DEFAULT '';
