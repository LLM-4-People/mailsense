-- Phase 2a: engine usage, the triage cache and runs, the single-run
-- lease, and user labels. (Unreleased: edited in review round 2a.)

-- Persisted usage counters per UTC day, provider and model (every call,
-- failed ones included: engine.UsageRecord).
CREATE TABLE engine_usage (
    day           TEXT NOT NULL,
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    calls         INTEGER NOT NULL DEFAULT 0,
    failures      INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (day, provider, model)
) STRICT;

-- The triage cache: RAW model answers (JSON) per message, question-spec
-- fingerprint and decider (provider/model). A hit needs the same state
-- hash too; priors are blended at read time, never stored. The answers may
-- be partial (a later chunk failed): the next run asks only the rest.
CREATE TABLE triage_decisions (
    message_id  INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    decider     TEXT NOT NULL,
    state_hash  TEXT NOT NULL,
    answers     TEXT NOT NULL,
    run_id      INTEGER NOT NULL,
    decided_at  TEXT NOT NULL,
    PRIMARY KEY (message_id, fingerprint, decider)
) STRICT;

-- The last failure of a message under a spec and decider (cleared by a
-- later success); a failure never removes an earlier decision.
CREATE TABLE triage_failures (
    message_id  INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    decider     TEXT NOT NULL,
    error       TEXT NOT NULL,
    run_id      INTEGER NOT NULL,
    failed_at   TEXT NOT NULL,
    PRIMARY KEY (message_id, fingerprint, decider)
) STRICT;

CREATE TABLE triage_runs (
    id               INTEGER PRIMARY KEY,
    started_at       TEXT NOT NULL,
    finished_at      TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL,
    decider          TEXT NOT NULL,
    fingerprint      TEXT NOT NULL,
    report           TEXT NOT NULL DEFAULT '{}',
    error            TEXT NOT NULL DEFAULT ''
) STRICT;

-- One holder at a time per name (a triage run: two would bill the same
-- messages twice). A crashed holder's lease expires.
CREATE TABLE leases (
    name       TEXT PRIMARY KEY,
    holder     TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;

-- User labels (ground truth, few-shot examples, suspicious priors). The
-- sender and subject come from the message: never copied.
CREATE TABLE labels (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    question   TEXT NOT NULL,
    value      TEXT NOT NULL,
    source     TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (message_id, question, source)
) STRICT;
