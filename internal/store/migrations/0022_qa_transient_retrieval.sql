-- A retrieval failure is not an answer. Older builds saved it to qa_history,
-- so the Ask screen kept replaying "the search index is preparing" long after
-- the index was complete. Such a row has no answer, no provider and no
-- citation: there is nothing to keep, and new transient notices are never
-- saved. Answers are untouched.
DELETE FROM qa_history WHERE notice = 'qa_index_warming';
