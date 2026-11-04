-- Review round L0: what a connector changed while reading a message
-- (mail.Alteration, JSON array), the calendar METHOD of a message's
-- calendar part (RFC 5546), and the alterations counted per sync run
-- (JSON object: code -> count). Nothing changed or dropped is silent.
ALTER TABLE messages ADD COLUMN calendar_method TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN alterations TEXT NOT NULL DEFAULT '[]';
ALTER TABLE sync_runs ADD COLUMN altered TEXT NOT NULL DEFAULT '{}';

-- Review round L0 (#6): the selection applies to the store at once. A
-- folder carries its store's key (source.StoreKey, computed by the Go
-- function mailsense_store_key) and whether that store is picked
-- (maintained by store.applySelection from mailboxes.selected); every
-- query over the user's mail reads active AND picked folders only.
ALTER TABLE folders ADD COLUMN store_key TEXT NOT NULL DEFAULT '';
ALTER TABLE folders ADD COLUMN picked INTEGER NOT NULL DEFAULT 0;
UPDATE folders SET store_key = mailsense_store_key(source_id, store_id);
CREATE INDEX folders_store_key ON folders(store_key);

-- Review round L0 (rules §1.9): declared addresses live in the
-- configuration only (mailboxes.declared_addresses), joined at read time;
-- owner_identities keeps what the sources reported.
DELETE FROM owner_identities WHERE kind = 'declared';

-- Review round L0 (#1): whether the last discovery read a mailbox's owner
-- identity completely (an offline or partly read store keeps the
-- recorded one).
ALTER TABLE mailboxes ADD COLUMN identity_complete INTEGER NOT NULL DEFAULT 0;
