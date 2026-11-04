-- The triage planner's failure record now carries what a quarantine needs:
-- the message revision and answer context the attempt ran under, how many
-- consecutive failures of one class happened there, and whether the provider
-- rejected the message itself (engine.ErrRejected) instead of failing
-- provider-wide. The planner leaves a permanently rejected message out after
-- MaxPermanentFailures attempts at the same inputs; the failure row stays, so
-- Failed coverage still counts it.
--
-- Rows written before this migration stay visible and are not quarantined
-- (attempts 0, permanent 0): the migration never guesses a class from an
-- error string; the next failure records the real one at attempt 1. A new
-- message revision, profile, decider, route or preview also clears it.
ALTER TABLE triage_failures ADD COLUMN message_revision INTEGER NOT NULL DEFAULT -1;
ALTER TABLE triage_failures ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE triage_failures ADD COLUMN permanent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE triage_failures ADD COLUMN context TEXT NOT NULL DEFAULT '';
UPDATE triage_failures SET message_revision = COALESCE((SELECT revision FROM messages WHERE id = message_id), -1);
