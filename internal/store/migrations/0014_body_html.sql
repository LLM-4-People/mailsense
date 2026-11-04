-- Display-only HTML alternative of a message (the plain body stays the one
-- learning/search body). Raw: it is sanitized on every API read by
-- internal/render, never returned as stored and never indexed.
--
-- body_html_known=false means the source has not declared an HTML state for
-- this message yet: either it was stored before this column existed (the
-- backfill job), or the connector cannot read HTML. A write that does not
-- know HTML never erases a known one (store writer).

ALTER TABLE messages ADD COLUMN body_html TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN body_html_known INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN body_html_truncated INTEGER NOT NULL DEFAULT 0;
