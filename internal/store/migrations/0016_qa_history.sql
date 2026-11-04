-- Grounded Q&A history: one row per successful answer (structured,
-- generated or retrieval-only). Questions and answers are the owner's
-- content and never leave this local database through logs. Citations
-- store only the message id and the reason; subject, sender and date are
-- rehydrated from visible owner mail when the history is read.

CREATE TABLE qa_history (
    id         INTEGER PRIMARY KEY,
    question   TEXT    NOT NULL,
    answer     TEXT    NOT NULL,
    mode       TEXT    NOT NULL,
    intent     TEXT    NOT NULL,
    counts     TEXT    NOT NULL DEFAULT '{}',
    citations  TEXT    NOT NULL,
    provider   TEXT    NOT NULL DEFAULT '',
    model      TEXT    NOT NULL DEFAULT '',
    language   TEXT    NOT NULL,
    preview    INTEGER NOT NULL,
    created_at TEXT    NOT NULL
) STRICT;

-- The history reads newest first; the store keeps the newest 100 rows.
CREATE INDEX qa_history_id_desc ON qa_history(id DESC);
