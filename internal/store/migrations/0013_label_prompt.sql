-- Provenance of a judge label: the prompt/taxonomy digest that produced it
-- (manual labels and pre-migration rows carry ''). A run skips a message
-- and question whose judge label was written under the same digest: the
-- same prompt is never bought twice.

ALTER TABLE labels ADD COLUMN prompt TEXT NOT NULL DEFAULT '';
