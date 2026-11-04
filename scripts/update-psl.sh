#!/usr/bin/env bash
# Update the shipped Public Suffix List (internal/psl): download the list
# at a pinned commit of github.com/publicsuffix/list, record the source,
# commit, date, size and SHA-256 in internal/psl/pin.json. Reproducible:
# the same commit always gives the same file (checked against pin.json
# when re-run without arguments).
#
#   scripts/update-psl.sh            re-download the pinned commit and verify it
#   scripts/update-psl.sh <commit>   pin another commit (full 40-hex sha)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
command -v jq >/dev/null || die "jq is required"
dir="$ROOT/internal/psl"
pin="$dir/pin.json"
commit="${1:-$(jq -r .commit "$pin")}"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "commit must be a full sha"
url="https://raw.githubusercontent.com/publicsuffix/list/$commit/public_suffix_list.dat"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp"
sha="$(sha256sum "$tmp" | cut -d' ' -f1)"
if (($# == 0)); then
	[[ "$sha" == "$(jq -r .sha256 "$pin")" ]] || die "the pinned commit no longer gives the pinned hash"
fi
date="$(curl -fsSL "https://api.github.com/repos/publicsuffix/list/commits/$commit" | jq -r .commit.committer.date)"
mv "$tmp" "$dir/public_suffix_list.dat"
trap - EXIT
jq -n --arg src "https://github.com/publicsuffix/list" --arg url "$url" --arg c "$commit" --arg d "$date" --arg s "$sha" \
	--argjson b "$(stat -c %s "$dir/public_suffix_list.dat")" --arg l "MPL-2.0" \
	'{source: $src, url: $url, commit: $c, committed_at: $d, sha256: $s, bytes: $b, license: $l}' >"$pin"
say "public suffix list $commit ($date) $sha"
