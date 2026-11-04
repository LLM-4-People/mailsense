-- The explicit answer route is a per-turn fact. Older rows answered with the
-- historical computed-or-provider behavior, which is Auto.
ALTER TABLE qa_history ADD COLUMN route TEXT NOT NULL DEFAULT 'auto';
