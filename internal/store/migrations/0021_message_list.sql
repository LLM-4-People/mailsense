-- The mail-list read orders by one materialized instant instead of the
-- CASE expression, so keyset pagination can use an index. instant is
-- mail.Instant: a sent item's sent_on when present, its received_at
-- otherwise; any other folder's received_at. The single write path
-- computes it from mail.Instant, and this backfill applies the same rule
-- to mail stored before the column existed.
--
-- The column is derived, not a mail.Message field: it stays outside the
-- JSON-column mapping and is written explicitly beside the display-only
-- HTML columns.

ALTER TABLE messages ADD COLUMN instant TEXT NOT NULL DEFAULT '';

UPDATE messages
SET instant = CASE
    WHEN (SELECT f.role FROM folders f WHERE f.id = messages.folder_id) = 'sent'
        AND sent_on <> ''
        THEN sent_on
    ELSE received_at
END;

-- One index serves the whole mailbox (descending keyset order), the other
-- serves a single folder's page. Both end in id so equal instants have a
-- stable, unique position.
CREATE INDEX messages_instant ON messages(instant DESC, id DESC);
CREATE INDEX messages_folder_instant ON messages(folder_id, instant DESC, id DESC);
