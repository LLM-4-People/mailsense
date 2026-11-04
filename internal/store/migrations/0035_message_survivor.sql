-- messages_sender indexes the raw address, but no query reads it: address
-- matching uses messages_sender_norm and messages_sender_domain (0031), and
-- every insert was maintaining the raw index for nothing. The move-survivor
-- join reads one same-store row by its Internet Message-ID, so the composite
-- key gives that lookup a single index instead of a scan.
DROP INDEX IF EXISTS messages_sender;
CREATE INDEX IF NOT EXISTS messages_survivor
  ON messages(source_id, store_id, internet_message_id);
