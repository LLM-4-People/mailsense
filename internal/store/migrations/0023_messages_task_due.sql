-- Due-date reads (Agenda, commitments, company profiles, the brief) ask for
-- the few messages with an open follow-up in a window, but the planner used
-- to start from scope_verdicts(verdict='owner') and visit every owner
-- message. A partial index contains only messages that carry a due date
-- (a few dozen on a 33k mailbox), so the same window becomes an index range
-- probe. SQLite maintains it transactionally on every message write.
CREATE INDEX messages_task_due ON messages(task_due_date)
WHERE task_due_date <> '';
