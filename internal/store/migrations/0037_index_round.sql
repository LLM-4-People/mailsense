-- Index round: one dead index removed, one shaped for its query added, and
-- five indexes no query can use dropped. Each drop was proved by searching the
-- whole tree for the index name and for a predicate it could serve; the
-- consumer that used to justify it is gone.
--
-- Added: the display backfill's own key. BackfillNewest filters
--   source_id = ? AND (body_html_known = 0 OR (inline_assets_checked = 0 AND has_inline_refs = 1))
-- and orders newest first by (instant, id). messages_display_pending led with
-- body_html_known and carried no instant, so the query could not use it and
-- instead scanned messages_survivor and sorted the whole mailbox: 324 ms for
-- one 200-row page on a 33,842-message snapshot. This partial index has the
-- filter as its predicate and the sort as its key, so the page is an index
-- range read with no temp B-tree.
--
-- Dropped:
--   messages_display_pending    the index this one replaces; it never served
--                               the backfill's OR predicate or its ordering.
--   messages_internet_id        superseded by messages_survivor, which serves
--                               the only internet_message_id predicate
--                               (moveSurvivorMatch) with source_id/store_id.
--   qa_history_id_desc          duplicates the INTEGER PRIMARY KEY B-tree; the
--                               history read scans it with no temp sort either
--                               way on a table capped at 100 rows.
--   contacts_name               Contacts() plans contacts_store plus a sort;
--                               the folded search is an instr() that no index
--                               can serve.
--   calendars_store             calendars are read by (source_id, store_id,
--                               entry_id), which the primary key covers.
--   calendar_events_global      the window read uses calendar_events_window.
--   event_attendees_norm        attendees are read by event_id.
CREATE INDEX messages_backfill ON messages(source_id, instant DESC, id DESC)
    WHERE body_html_known = 0 OR (inline_assets_checked = 0 AND has_inline_refs = 1);

DROP INDEX messages_display_pending;
DROP INDEX messages_internet_id;
DROP INDEX qa_history_id_desc;
DROP INDEX contacts_name;
DROP INDEX calendars_store;
DROP INDEX calendar_events_global;
DROP INDEX event_attendees_norm;
