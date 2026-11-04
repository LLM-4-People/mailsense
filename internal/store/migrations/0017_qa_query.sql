-- Typed Ask answers: a computed plan/result and a clarifying question are
-- part of the answer, not prose. The plan cache keys a SHA-256 of the
-- normalized question, so the cache itself carries no question text.
-- Existing history rows keep empty strings; the reader treats them as nil.

ALTER TABLE qa_history ADD COLUMN plan TEXT NOT NULL DEFAULT '';
ALTER TABLE qa_history ADD COLUMN result TEXT NOT NULL DEFAULT '';
ALTER TABLE qa_history ADD COLUMN clarify TEXT NOT NULL DEFAULT '';
ALTER TABLE qa_history ADD COLUMN notice TEXT NOT NULL DEFAULT '';

CREATE TABLE qa_plan_cache (
    question_key TEXT PRIMARY KEY,
    language     TEXT NOT NULL,
    plan         TEXT NOT NULL,
    created_at   TEXT NOT NULL
) STRICT;

CREATE INDEX qa_plan_cache_created ON qa_plan_cache(created_at);
