-- Materialized inline-reference marker. The display backfill must find old
-- mail whose HTML references an inline image without scanning every stored
-- HTML body on every batch; this column is written with the message and
-- filled once here for rows stored before it existed.
ALTER TABLE messages ADD COLUMN has_inline_refs INTEGER NOT NULL DEFAULT 0;

UPDATE messages SET has_inline_refs = 1
WHERE body_html_known = 1
  AND (body_html LIKE '%cid:%'
       OR EXISTS (SELECT 1 FROM attachments a
                  WHERE a.message_id = messages.id
                    AND (a.content_id <> '' OR a.content_location <> '')));

CREATE INDEX messages_display_pending
    ON messages(source_id, body_html_known, inline_assets_checked, has_inline_refs);
