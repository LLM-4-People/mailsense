-- The conversation digest groups every owner message by conversation and
-- needs each conversation's members newest first. The plain conversation
-- index forces a temporary sort over the whole mailbox; this one carries the
-- instant and row id so the window and the aggregate can read in index order.
-- `messages.instant` is the materialized UTC mail instant written by the one
-- message write path, and the digest's old CASE expression produced the same
-- value. Migrations are append-only: schema version 24 was applied to the
-- benchmark store, so the file stays even though the current planner does not
-- always choose it.
CREATE INDEX messages_conversation_instant ON messages(conversation_id, instant DESC, id DESC);
