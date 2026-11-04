-- Phase 1a schema. Enumerations (folder roles, recipient kinds, verdicts,
-- reasons) are validated in Go at the store's write paths (UpsertMessages,
-- UpsertFolder, ReplaceScope), not duplicated here as CHECKs.

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE sources (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE folders (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    store_id    TEXT NOT NULL,
    store_name  TEXT NOT NULL,
    store_kind  TEXT NOT NULL,
    store_owner TEXT NOT NULL,
    path        TEXT NOT NULL,
    role        TEXT NOT NULL,
    item_count  INTEGER,
    remote_id   TEXT NOT NULL,
    -- 0: no longer discovered but still existing (store disabled, folder
    -- excluded): its mail is kept, out of scope and statistics.
    active      INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT NOT NULL,
    UNIQUE (source_id, store_id, path)
) STRICT;

-- Column names after folder_id mirror the json tags of mail.Message
-- (store/columns.go maps them; a test keeps both in sync).
CREATE TABLE messages (
    id                        INTEGER PRIMARY KEY,
    source_id                 TEXT NOT NULL,
    store_id                  TEXT NOT NULL,
    msg_id                    TEXT NOT NULL,
    folder_id                 INTEGER NOT NULL REFERENCES folders(id),
    entry_id                  TEXT NOT NULL,
    long_term_entry_id        TEXT NOT NULL,
    search_key                TEXT NOT NULL,
    origin                    TEXT NOT NULL,
    subject                   TEXT NOT NULL,
    sender_name               TEXT NOT NULL,
    sender_address            TEXT NOT NULL,
    sent_representing_address TEXT NOT NULL,
    addressed_to_me           TEXT NOT NULL,
    received_at               TEXT NOT NULL,
    sent_on                   TEXT NOT NULL,
    creation_time             TEXT NOT NULL,
    last_modification_time    TEXT NOT NULL,
    last_verb_execution_time  TEXT NOT NULL,
    last_verb_executed        INTEGER,
    local_commit_time         TEXT NOT NULL,
    message_flags             INTEGER,
    is_read                   INTEGER NOT NULL,
    has_attachments           INTEGER NOT NULL,
    importance                INTEGER,
    sensitivity               INTEGER,
    size                      INTEGER,
    spam_confidence           INTEGER,
    categories                TEXT NOT NULL,
    flag_status               INTEGER,
    flag_request              TEXT NOT NULL,
    task_due_date             TEXT NOT NULL,
    task_start_date           TEXT NOT NULL,
    reminder_set              INTEGER NOT NULL,
    reminder_time             TEXT NOT NULL,
    read_receipt_requested    INTEGER NOT NULL,
    voting_options            TEXT NOT NULL,
    voting_response           TEXT NOT NULL,
    message_class             TEXT NOT NULL,
    internet_message_id       TEXT NOT NULL,
    in_reply_to               TEXT NOT NULL,
    message_references        TEXT NOT NULL,
    conversation_id           TEXT NOT NULL,
    conversation_index        TEXT NOT NULL,
    headers                   TEXT NOT NULL,
    body                      TEXT NOT NULL,
    body_truncated            INTEGER NOT NULL,
    stamp                     TEXT NOT NULL,
    partial                   INTEGER NOT NULL,
    first_seen_at             TEXT NOT NULL,
    updated_at                TEXT NOT NULL,
    last_seen_run             INTEGER,
    UNIQUE (source_id, store_id, msg_id)
) STRICT;

CREATE INDEX messages_folder_run   ON messages(folder_id, last_seen_run);
CREATE INDEX messages_received     ON messages(received_at);
CREATE INDEX messages_sender       ON messages(sender_address);
CREATE INDEX messages_conversation ON messages(conversation_id);
CREATE INDEX messages_internet_id  ON messages(internet_message_id);
-- Messages still waiting for their detail pass, newest first (store.PendingDetails).
CREATE INDEX messages_pending      ON messages(source_id, received_at) WHERE partial = 1;

CREATE TABLE recipients (
    message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    position     INTEGER NOT NULL,
    kind         TEXT NOT NULL,
    name         TEXT NOT NULL,
    address      TEXT NOT NULL,
    address_norm TEXT NOT NULL,
    PRIMARY KEY (message_id, position)
) STRICT, WITHOUT ROWID;

CREATE INDEX recipients_address ON recipients(address_norm);

-- rowid = messages.id; written only by store.writeFTS. Contentless (the text
-- already lives in messages; a content copy cost 124 MB on the 33,842-message
-- fixture) with contentless_delete so an entry can be replaced by rowid.
CREATE VIRTUAL TABLE messages_fts USING fts5(
    subject, body, sender, recipients,
    content = '',
    contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE sync_cursors (
    folder_id  INTEGER PRIMARY KEY REFERENCES folders(id) ON DELETE CASCADE,
    cursor     TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE sync_runs (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    seen        INTEGER NOT NULL DEFAULT 0,
    written     INTEGER NOT NULL DEFAULT 0,
    skipped     INTEGER NOT NULL DEFAULT 0,
    duplicates  INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE scope_verdicts (
    message_id  INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    verdict     TEXT NOT NULL,
    reason      TEXT NOT NULL,
    computed_at TEXT NOT NULL
) STRICT;

CREATE INDEX scope_verdicts_verdict ON scope_verdicts(verdict);

-- One owner identity per mailbox store (PORTING §2): a delegate store has
-- its own owner, never the user's.
CREATE TABLE owner_identities (
    source_id   TEXT NOT NULL,
    store_id    TEXT NOT NULL,
    address     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    computed_at TEXT NOT NULL,
    PRIMARY KEY (source_id, store_id, address)
) STRICT;

-- Removals held by the syncer's safety valve (a snapshot that suddenly
-- lacks many messages: a shrunken local cache, an OST rebuild). Applied once
-- they persisted for sync.removal.auto_apply_after, or when confirmed.
CREATE TABLE held_removals (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    folder_id  INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    held_at    TEXT NOT NULL
) STRICT;

CREATE INDEX held_removals_folder ON held_removals(folder_id);
