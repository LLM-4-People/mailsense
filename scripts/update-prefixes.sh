#!/usr/bin/env bash
# Regenerate internal/mail/subject_prefixes.json (the shipped reply and
# forward prefixes) from a PINNED revision of Wikipedia's "List of email
# subject abbreviations"; the source revision, its SHA-256 and the output's
# are recorded in internal/mail/prefixes-pin.json (a test checks the
# shipped file against it). The hand-kept extras are carried over.
#
#   scripts/update-prefixes.sh           re-download the pinned revision, verify it, regenerate
#   scripts/update-prefixes.sh <oldid>   pin another revision (the page history's oldid)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
command -v jq >/dev/null || die "jq is required"
dir="$ROOT/internal/mail"
pin="$dir/prefixes-pin.json"
title="List_of_email_subject_abbreviations"
oldid="${1:-$(jq -r .oldid "$pin")}"
[[ "$oldid" =~ ^[0-9]+$ ]] || die "oldid must be a number"
url="https://en.wikipedia.org/w/index.php?title=$title&oldid=$oldid&action=raw"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp"
sha="$(sha256sum "$tmp" | cut -d' ' -f1)"
if (($# == 0)); then
	[[ "$sha" == "$(jq -r .sha256 "$pin")" ]] || die "the pinned revision no longer gives the pinned hash"
fi
at="$(curl -fsSL "https://en.wikipedia.org/w/api.php?action=query&prop=revisions&revids=$oldid&rvprop=timestamp&format=json" |
	jq -r '.query.pages[].revisions[0].timestamp')"
(cd "$ROOT" && "$GO" run ./scripts/datagen prefixes "$tmp" "$dir")
out="$(sha256sum "$dir/subject_prefixes.json" | cut -d' ' -f1)"
jq -n --arg u "$url" --arg o "$oldid" --arg at "$at" --arg s "$sha" --arg out "$out" \
	'{source: "https://en.wikipedia.org/wiki/List_of_email_subject_abbreviations", url: $u, oldid: ($o | tonumber),
	  revision_at: $at, sha256: $s, license: "CC-BY-SA-4.0", generator: "scripts/datagen (prefixes)",
	  outputs: {"subject_prefixes.json": $out}}' >"$pin"
say "subject prefixes from revision $oldid ($at)"
