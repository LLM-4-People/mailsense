-- The certified route's answers (internal/route), one row per message and
-- profile. A paid decider's cache and the route are never mixed: different
-- table, different key. The answers column is JSON: question → route.Answer
-- (one entry per question, an abstention included).

CREATE TABLE route_answers (
    message_id  INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,    -- the triage profile's fingerprint (the route's one key)
    revision    INTEGER NOT NULL, -- the message revision the answers were computed at
    answers     TEXT NOT NULL,
    run_id      INTEGER NOT NULL, -- the layer1 run that wrote them
    decided_at  TEXT NOT NULL,
    PRIMARY KEY (message_id, fingerprint)
) STRICT;

-- The reads are by fingerprint (a whole pass, a reader's message set).
CREATE INDEX route_answers_by_fingerprint ON route_answers(fingerprint, message_id);
