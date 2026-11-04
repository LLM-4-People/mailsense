-- Phase L0 (#5): search for scripts searched by substring. A second
-- contentless FTS5 index with the trigram tokenizer (substring matching)
-- holds the messages whose text has letters of a substring-searched
-- script (the spaceless ones — Han, Kana, Thai, Lao, Khmer… — plus the
-- hand-kept substring_extra: Hangul, whose words carry particles, and the
-- matra-based Indic scripts, whose dependent vowel signs unicode61 treats
-- as separators: mail/scripts.json); both indexes store the folded form
-- (mail.Fold). Comment corrected in review round L0, round 30 (Indic) and
-- round 31 (the split text/coverage versions); the statements are
-- unchanged. Written by store.writeFTS for message writes and by
-- store.reindexTri for the coverage-only pass. Messages written before
-- this migration are re-indexed by store.Open (meta fts_text_version and
-- fts_coverage_version, mirrored in fts_version for older builds).
CREATE VIRTUAL TABLE messages_fts_tri USING fts5(
    subject, body, sender, recipients,
    content = '',
    contentless_delete = 1,
    tokenize = 'trigram'
);
