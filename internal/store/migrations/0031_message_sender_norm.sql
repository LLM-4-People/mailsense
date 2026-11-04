-- The All-mail and search sender filters compared a normalized expression on
-- every row, so an exact-sender page scanned the whole owner population and
-- sorted it (p95 ~50ms at 34k messages). The sender keys are now stored and
-- indexed with the page order, so the filter is one range read.
--
-- sender_norm is mail.NormalizeAddress(sender_address) and sender_domain is
-- mail.AddressDomain(sender_norm); mailsense_lower and
-- mailsense_address_domain are the SQL forms of the same two rules, so the
-- backfill and the writer cannot disagree. The one write path maintains both
-- columns on every upsert.

ALTER TABLE messages ADD COLUMN sender_norm TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN sender_domain TEXT NOT NULL DEFAULT '';

UPDATE messages
SET sender_norm = mailsense_lower(sender_address),
    sender_domain = mailsense_address_domain(mailsense_lower(sender_address));

-- Both end in the keyset order, so a sender page is returned in order
-- without a temporary sort.
CREATE INDEX messages_sender_norm ON messages(sender_norm, instant DESC, id DESC);
CREATE INDEX messages_sender_domain ON messages(sender_domain, instant DESC, id DESC);
