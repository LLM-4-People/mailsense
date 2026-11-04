-- Phase L0 (#9): what a plain RFC 5322 source carries that Outlook's
-- model had no place for. Written only by the message write path
-- (store.writer), replaced with every write of the message.

-- Attachment metadata (never the content).
CREATE TABLE attachments (
    message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    position     INTEGER NOT NULL,
    name         TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size         INTEGER NOT NULL,
    inline       INTEGER NOT NULL,
    PRIMARY KEY (message_id, position)
) STRICT, WITHOUT ROWID;

-- The further folders of a message (a Gmail label): messages.folder_id is
-- its first folder, these are the others.
CREATE TABLE message_folders (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    folder_id  INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, folder_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX message_folders_folder ON message_folders(folder_id);
