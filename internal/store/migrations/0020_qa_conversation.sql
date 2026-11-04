-- One Ask question can continue a short chat thread. Every turn of a
-- conversation stores the same opaque, service-generated conversation id;
-- an empty id is a standalone turn written before conversations existed.
-- The id is not a mailbox conversation id: it only groups Q&A history and
-- scopes the planner context and the caches.

ALTER TABLE qa_history ADD COLUMN conversation_id TEXT NOT NULL DEFAULT '';
CREATE INDEX qa_history_conversation ON qa_history(conversation_id, id);
