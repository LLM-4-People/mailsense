-- A message can move between two folders while a sync run reads the folders
-- one after another: the destination folder may be snapshotted before the
-- move and the source folder after it, so both complete snapshots omit the
-- message and no destination row exists yet. RemoveMessages would find no
-- survivor and delete the row with its labels.
--
-- A row here means "this absence has no survivor yet; do not delete it before
-- the next complete run". Separately from held_removals (the mass-removal
-- valve, which waits for owner confirmation or a long backstop), a move hold
-- is a one-run policy: the next run either finds the destination row (a move,
-- labels copied) or removes a genuinely deleted message.
CREATE TABLE move_holds (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    folder_id  INTEGER NOT NULL,
    held_run   INTEGER NOT NULL
) STRICT;

CREATE INDEX move_holds_by_folder ON move_holds(folder_id);
