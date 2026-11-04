-- The layer-1 preview answers: the threshold-free best guess of the
-- question's active local model (internal/selective.Given), one row per
-- message and profile. It is generated display data for the explicit
-- learning.test_mode preview only: never a certification source, never
-- read by the route, the gate, the judge or the report. The route's own
-- answers stay in route_answers; the two tables are never mixed.

CREATE TABLE route_preview_answers (
    message_id  INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,    -- the triage profile's fingerprint (the route's one key)
    revision    INTEGER NOT NULL, -- the message revision the answers were computed at
    answers     TEXT NOT NULL,    -- JSON: question → engine.Answer in the Given shape (no abstain)
    run_id      INTEGER NOT NULL, -- the layer1 run that wrote them
    decided_at  TEXT NOT NULL,
    PRIMARY KEY (message_id, fingerprint)
) STRICT;

-- The reads are by fingerprint (a whole pass, a reader's message set).
CREATE INDEX route_preview_answers_by_fingerprint ON route_preview_answers(fingerprint, message_id);
