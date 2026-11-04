#!/usr/bin/env bash
# Regenerate the shipped Unicode/CLDR-derived data of internal/mail
# (scripts.json spaceless, fold.json letters, punctuation.json question
# marks) from PINNED sources: the Unicode Character Database of one
# version and CLDR at one commit. Inputs and outputs are recorded with
# their SHA-256 in internal/mail/unicode-pin.json; a test checks the
# shipped files against it. The hand-kept keys (scripts.json
# substring_extra, fold.json strip_marks, the _doc texts) are carried over.
#
#   scripts/update-unicode-data.sh                       re-download the pinned inputs, verify them, regenerate
#   scripts/update-unicode-data.sh <ucd-version> <cldr-tag>   pin other versions (e.g. 17.0.0 release-48-2);
#                                                        the UCD version must equal Go's unicode.Version
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
command -v jq >/dev/null || die "jq is required"
dir="$ROOT/internal/mail"
pin="$dir/unicode-pin.json"
ucd_files=(UnicodeData.txt PropList.txt Scripts.txt PropertyValueAliases.txt CaseFolding.txt)
declare -A cldr_files=([scriptMetadata.txt]=common/properties/scriptMetadata.txt [Latin-ASCII.xml]=common/transforms/Latin-ASCII.xml)

if (($# == 0)); then
	[[ -f "$pin" ]] || die "no $pin: pass <ucd-version> <cldr-tag>"
	ucd="$(jq -r .ucd.version "$pin")" tag="$(jq -r .cldr.tag "$pin")" commit="$(jq -r .cldr.commit "$pin")"
elif (($# == 2)); then
	ucd="$1" tag="$2"
	commit="$(curl -fsSL "https://api.github.com/repos/unicode-org/cldr/commits/$tag" | jq -r .sha)"
else
	die "usage: $0 [<ucd-version> <cldr-tag>]"
fi
[[ "$ucd" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "bad UCD version: $ucd"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "CLDR tag $tag did not resolve to a commit"
src="$(mktemp -d)"
trap 'rm -rf "$src"' EXIT
ucd_url="https://www.unicode.org/Public/$ucd/ucd"
cldr_url="https://raw.githubusercontent.com/unicode-org/cldr/$commit"
for f in "${ucd_files[@]}"; do curl -fsSL "$ucd_url/$f" -o "$src/$f"; done
for f in "${!cldr_files[@]}"; do curl -fsSL "$cldr_url/${cldr_files[$f]}" -o "$src/$f"; done

hashes() { # <dir> <files…>: a JSON object name → SHA-256
	local d="$1"
	shift
	for f in "$@"; do jq -n --arg k "$f" --arg v "$(sha256sum "$d/$f" | cut -d' ' -f1)" '{($k): $v}'; done | jq -s 'add'
}
inputs="$(hashes "$src" "${ucd_files[@]}" "${!cldr_files[@]}")"
if (($# == 0)); then
	[[ "$(jq -S .inputs "$pin")" == "$(jq -S . <<<"$inputs")" ]] || die "the pinned sources no longer give the pinned hashes"
fi
(cd "$ROOT" && "$GO" run ./scripts/datagen unicode "$src" "$dir")
outputs="$(hashes "$dir" scripts.json fold.json punctuation.json)"
jq -n --arg ucd "$ucd" --arg uurl "$ucd_url" --arg tag "$tag" --arg c "$commit" --arg curl "$cldr_url" \
	--argjson in "$inputs" --argjson out "$outputs" \
	'{ucd: {version: $ucd, url: $uurl}, cldr: {tag: $tag, commit: $c, url: $curl}, license: "Unicode-3.0",
	  generator: "scripts/datagen (unicode)", inputs: $in, outputs: $out}' >"$pin"
say "unicode data from UCD $ucd and CLDR $tag ($commit)"
