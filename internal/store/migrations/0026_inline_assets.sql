-- Inline display assets: the MIME identity of one attachment and the
-- content-addressed bytes of the image parts an HTML body references.
-- These columns are display-only: they never enter learning, search or the
-- message revision. A blob is written once per SHA-256 and shared by every
-- message that carries the same signature logo or spacer.
ALTER TABLE attachments ADD COLUMN content_id TEXT NOT NULL DEFAULT '';
ALTER TABLE attachments ADD COLUMN content_location TEXT NOT NULL DEFAULT '';
ALTER TABLE attachments ADD COLUMN sha256 TEXT NOT NULL DEFAULT '';

CREATE TABLE attachment_blobs (
    sha256       TEXT NOT NULL PRIMARY KEY,
    content_type TEXT NOT NULL,
    size         INTEGER NOT NULL,
    bytes        BLOB NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX attachments_sha256 ON attachments(sha256) WHERE sha256 <> '';
