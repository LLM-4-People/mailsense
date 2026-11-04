-- The non-mail half of the world model: the owner's address book and the
-- calendars visible in the profile (own, shared, delegate). Events are
-- stored as bounded occurrences in UTC; a recurring series is expanded by
-- the connector inside the configured window, never stored as an unbounded
-- rule the ranking layer would have to interpret.

CREATE TABLE contacts (
    id             INTEGER PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    store_id       TEXT NOT NULL,
    store_key      TEXT NOT NULL,
    entry_id       TEXT NOT NULL,
    revision       TEXT NOT NULL DEFAULT '',
    display_name   TEXT NOT NULL DEFAULT '',
    company        TEXT NOT NULL DEFAULT '',
    department     TEXT NOT NULL DEFAULT '',
    job_title      TEXT NOT NULL DEFAULT '',
    folder_path    TEXT NOT NULL DEFAULT '',
    last_modified  TEXT NOT NULL DEFAULT '',
    UNIQUE (source_id, store_id, entry_id)
);
CREATE INDEX contacts_store ON contacts(store_key);
CREATE INDEX contacts_name ON contacts(display_name);

-- One row per address of a contact (Outlook Email1/2/3, exchange aliases).
-- The normalized form is the only value joins and lookups use.
CREATE TABLE contact_emails (
    contact_id    INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    ordinal       INTEGER NOT NULL,
    address       TEXT NOT NULL,
    address_norm  TEXT NOT NULL,
    PRIMARY KEY (contact_id, ordinal)
);
CREATE INDEX contact_emails_norm ON contact_emails(address_norm);

CREATE TABLE calendars (
    id           INTEGER PRIMARY KEY,
    source_id    TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    store_id     TEXT NOT NULL,
    store_key    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    is_default   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (source_id, store_id, entry_id)
);
CREATE INDEX calendars_store ON calendars(store_key);

-- One occurrence: the connector expands a recurrence inside the sync
-- window and stores each occurrence with its own start/end. entry_id plus
-- start_at identifies an occurrence; a moved occurrence updates in place.
CREATE TABLE calendar_events (
    id               INTEGER PRIMARY KEY,
    calendar_id      INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    source_id        TEXT NOT NULL,
    store_key        TEXT NOT NULL,
    entry_id         TEXT NOT NULL,
    global_id        TEXT NOT NULL DEFAULT '',
    clean_global_id  TEXT NOT NULL DEFAULT '',
    subject          TEXT NOT NULL DEFAULT '',
    location         TEXT NOT NULL DEFAULT '',
    organizer_name   TEXT NOT NULL DEFAULT '',
    organizer_address TEXT NOT NULL DEFAULT '',
    start_at         TEXT NOT NULL,  -- UTC instant
    end_at           TEXT NOT NULL,  -- UTC instant, exclusive
    all_day          INTEGER NOT NULL DEFAULT 0,
    busy_status      INTEGER NOT NULL DEFAULT 0, -- 0 free, 1 tentative, 2 busy, 3 out of office
    meeting_status   INTEGER NOT NULL DEFAULT 0, -- 0 none, 1 meeting, 2 received, 3 canceled, 4 forwarded
    response_status  INTEGER NOT NULL DEFAULT 0, -- 0 none, 1 organized, 2 tentative, 3 accepted, 4 declined, 5 not responded
    is_recurring     INTEGER NOT NULL DEFAULT 0,
    series_id        TEXT NOT NULL DEFAULT '',
    occurrence_index INTEGER NOT NULL DEFAULT 0,
    online_url       TEXT NOT NULL DEFAULT '',
    body_preview     TEXT NOT NULL DEFAULT '',
    last_modified    TEXT NOT NULL DEFAULT '',
    UNIQUE (source_id, store_key, entry_id, start_at)
);
CREATE INDEX calendar_events_window ON calendar_events(start_at, end_at);
CREATE INDEX calendar_events_global ON calendar_events(clean_global_id);

CREATE TABLE event_attendees (
    event_id        INTEGER NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL, -- required | optional | resource
    name            TEXT NOT NULL DEFAULT '',
    address         TEXT NOT NULL DEFAULT '',
    address_norm    TEXT NOT NULL DEFAULT '',
    response_status INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX event_attendees_norm ON event_attendees(address_norm);
