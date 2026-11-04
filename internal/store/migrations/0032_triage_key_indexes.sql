-- Every decision and failure read, count and prune asks by
-- (fingerprint, decider); the primary key starts with message_id and cannot
-- serve that prefix. The covering index also holds message_id and
-- decided_at, so the ordered read, the count/max and the key delete are one
-- index range instead of a table scan per teacher/key.
CREATE INDEX triage_decisions_by_key ON triage_decisions(fingerprint, decider, message_id, decided_at);
CREATE INDEX triage_failures_by_key ON triage_failures(fingerprint, decider);
