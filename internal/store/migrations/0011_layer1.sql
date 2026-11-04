-- Layer 1, the local per-install classifier (internal/layer1).

-- One trained head per question and version: the parameters (little-
-- endian float32, layer1.Model.Encode), what it was trained on (meta,
-- JSON: vector space, feature layout, λ, calibration, teacher counts) and
-- its out-of-fold measurement (metrics, JSON). At most one row per
-- question is active; a run's candidate replaces it only when its
-- measurement does not regress (layer1.promote.max_nll_regression), and a
-- failed or cancelled run never retires the active one.
CREATE TABLE layer1_models (
    id          INTEGER PRIMARY KEY,
    question    TEXT NOT NULL,
    version_key TEXT NOT NULL,
    kind        TEXT NOT NULL,
    classes     INTEGER NOT NULL,
    params      BLOB NOT NULL,
    meta        TEXT NOT NULL,
    metrics     TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('active', 'candidate', 'retired')),
    run_id      INTEGER NOT NULL,
    created_at  TEXT NOT NULL,
    UNIQUE (question, version_key)
) STRICT;

CREATE UNIQUE INDEX layer1_models_one_active ON layer1_models(question) WHERE state = 'active';

-- Layer-1 runs, with the fingerprint of their inputs (the stage compares
-- it with the current one).
CREATE TABLE layer1_runs (
    id          INTEGER PRIMARY KEY,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    inputs      TEXT NOT NULL DEFAULT '',
    report      TEXT NOT NULL DEFAULT '{}',
    error       TEXT NOT NULL DEFAULT ''
) STRICT;
