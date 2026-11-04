-- A calendar window replacement deletes each event's attendee rows by
-- event_id. Without an index that is a full scan per event, so replacing a
-- window cost O(events x attendees); the read side joined the same column.
CREATE INDEX event_attendees_event ON event_attendees(event_id);
