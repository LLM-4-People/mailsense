-- Repair the inline display state written by the first display pass.
--
-- 1. The backfill writes body_html directly and never updated the
--    materialized has_inline_refs flag (0028 filled it once, for the rows
--    that already had HTML). Fill it now for every later-written body, so a
--    repair can find those messages without scanning every HTML body.
UPDATE messages SET has_inline_refs = 1
WHERE has_inline_refs = 0
  AND body_html_known = 1
  AND instr(lower(body_html), 'cid:') > 0;

-- 2. The first pass never read inline parts Outlook labels
--    application/octet-stream with a .bin name, nor the image/jpg and
--    image/x-png aliases, because the shared allow-list did not recognize
--    them. Reset only the messages this build can repair; the next display
--    backfill re-reads them once and the current per-part budget still
--    applies. Deliberately unsupported parts (SVG, TIFF, video) and parts
--    over the budget are not reset: a re-read could not store them either.
UPDATE messages SET inline_assets_checked = 0
WHERE body_html_known = 1
  AND instr(lower(body_html), 'cid:') > 0
  AND EXISTS (
    SELECT 1 FROM attachments a
    WHERE a.message_id = messages.id
      AND a.inline = 1
      AND a.sha256 = ''
      AND lower(a.content_type) IN ('application/octet-stream', '',
                                    'image/jpg', 'image/pjpeg',
                                    'image/x-png', 'image/x-gif')
  );
