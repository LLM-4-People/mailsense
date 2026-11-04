-- The agenda is a view over the world model, persisted with the answer so
-- the history shows the same list that was computed. It carries no body.
ALTER TABLE qa_history ADD COLUMN agenda TEXT NOT NULL DEFAULT '';
