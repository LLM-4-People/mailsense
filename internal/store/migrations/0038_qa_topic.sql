-- The topic count answer: the catalogue path, the count, and the
-- classification coverage behind it, stored as one strict JSON object. A row
-- written before this migration has no topic answer and decodes to an empty
-- text, never to a zero that claims a topic was counted.
ALTER TABLE qa_history ADD COLUMN topic TEXT NOT NULL DEFAULT '';
