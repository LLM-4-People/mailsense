-- Layer 0 review: a message-only evidence value is reused only while the
-- message (its revision) AND its context are unchanged. context is the
-- digest of what such values read besides the message: the scope verdict
-- and reason, the store's owner identity, the source's capabilities and
-- the owners' organisations ('' for rows written before: recomputed).
ALTER TABLE evidence ADD COLUMN context TEXT NOT NULL DEFAULT '';

-- The reasons of not-applicable and unavailable values ("capability:
-- attachments", "no_headers"…), interned: a stored value names its
-- reason by id ({"u": 3}) instead of repeating the text in every row.
CREATE TABLE evidence_reasons (
    id     INTEGER PRIMARY KEY,
    reason TEXT NOT NULL UNIQUE
) STRICT;
