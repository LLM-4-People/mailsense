-- The address-book phone number in the source's display form. One string per
-- contact: business number preferred, mobile as the fallback. Contacts are
-- replaced on every full address-book read, so a later sync fills this in.
ALTER TABLE contacts ADD COLUMN phone TEXT NOT NULL DEFAULT '';
