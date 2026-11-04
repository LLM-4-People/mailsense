-- Display backfill completion marker. Existing rows start at 0 so the next
-- backfill can repair mail stored before inline assets existed; a writer that
-- knows the HTML state sets it to 1, and the backfill sets it after one
-- successful re-read even when the source had no bytes to give. Without the
-- marker, every old cid: message would be re-read on every pass.
ALTER TABLE messages ADD COLUMN inline_assets_checked INTEGER NOT NULL DEFAULT 0;
