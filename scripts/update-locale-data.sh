#!/usr/bin/env bash
# Regenerate the shipped language data of internal/locale (langid.json,
# locales.json, currency.json, confusables.json) from PINNED sources:
# CLDR at one commit (coverage levels, supplemental data, the main files of
# the chosen coverage levels, their regional locales and their parents), the UDHR in XML project at
# one commit (the translations the language profiles are trained on) and
# the Unicode security data (UTS #39 confusables) of one version. The
# generator's choices are internal/locale/params.json. Inputs and outputs
# are recorded with their SHA-256 in internal/locale/pin.json; a test
# checks the shipped files against it.
#
#   scripts/update-locale-data.sh                                   re-download the pinned inputs, verify, regenerate
#   scripts/update-locale-data.sh <cldr-tag> <udhr-commit> <unicode-version>   pin other sources
#
# LOCALE_KEEP=<dir> keeps the downloaded sources in <dir> (new, empty)
# instead of a temporary directory.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
command -v jq >/dev/null || die "jq is required"
dir="$ROOT/internal/locale"
pin="$dir/pin.json"

if (($# == 0)); then
	[[ -f "$pin" ]] || die "no $pin: pass <cldr-tag> <udhr-commit> <unicode-version>"
	tag="$(jq -r .cldr.tag "$pin")" commit="$(jq -r .cldr.commit "$pin")"
	udhr="$(jq -r .udhr.commit "$pin")" uv="$(jq -r .security.version "$pin")"
elif (($# == 3)); then
	tag="$1" udhr="$2" uv="$3"
	commit="$(curl -fsSL "https://api.github.com/repos/unicode-org/cldr/commits/$tag" | jq -r .sha)"
else
	die "usage: $0 [<cldr-tag> <udhr-commit> <unicode-version>]"
fi
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "CLDR tag $tag did not resolve to a commit"
[[ "$udhr" =~ ^[0-9a-f]{40}$ ]] || die "the UDHR commit must be a full SHA-1"
[[ "$uv" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "bad Unicode version: $uv"

if [[ -n "${LOCALE_KEEP:-}" ]]; then
	mkdir -p "$LOCALE_KEEP" && [[ -z "$(ls -A "$LOCALE_KEEP")" ]] || die "LOCALE_KEEP must name a new or empty directory"
	src="$(realpath "$LOCALE_KEEP")"
else
	src="$(mktemp -d)"
	trap 'rm -rf "$src"' EXIT
fi
mkdir -p "$src/cldr/main" "$src/udhr"
cldr_url="https://raw.githubusercontent.com/unicode-org/cldr/$commit/common"
udhr_url="https://codeload.github.com/eric-muller/udhr/tar.gz/$udhr"
sec_url="https://www.unicode.org/Public/$uv/security/confusables.txt"
for f in properties/coverageLevels.txt supplemental/supplementalData.xml supplemental/likelySubtags.xml supplemental/supplementalMetadata.xml; do
	curl -fsSL "$cldr_url/$f" -o "$src/cldr/$(basename "$f")"
done
# The chosen locales (params.json cldr_coverage), their regional locales
# (params.json regional_status) and their inheritance chains, root included
# (datagen locale-ids). A regional locale CLDR ships no file for (default
# content: it inherits everything) answers 404 and is skipped; any other
# failure stops the update.
locales="$(cd "$ROOT" && "$GO" run ./scripts/datagen locale-ids "$src" "$dir")"
fetch_main() {
	local code
	code="$(curl -sSL -w '%{http_code}' "$cldr_url/main/$1.xml" -o "$src/cldr/main/$1.xml")" || { echo "download of $1 failed" >&2; return 1; }
	case "$code" in
	200) ;;
	404) rm -f "$src/cldr/main/$1.xml" ;;
	*) echo "download of $1: HTTP $code" >&2; return 1 ;;
	esac
}
export -f fetch_main
export cldr_url src
xargs -P 8 -I{} bash -c 'fetch_main "$1"' _ {} <<<"$locales"
curl -fsSL "$udhr_url" -o "$src/udhr.tgz"
tar -xzf "$src/udhr.tgz" -C "$src" --wildcards '*/data/udhr/*.xml'
mv "$src"/udhr-*/data/udhr/*.xml "$src/udhr/"
curl -fsSL "$sec_url" -o "$src/confusables.txt"

inputs="$(cd "$src" && find cldr udhr confusables.txt -type f | sort | while read -r f; do
	jq -n --arg k "$f" --arg v "$(sha256sum "$f" | cut -d' ' -f1)" '{($k): $v}'
done | jq -s 'add')"
if (($# == 0)); then
	[[ "$(jq -S .inputs "$pin")" == "$(jq -S . <<<"$inputs")" ]] || die "the pinned sources no longer give the pinned hashes"
fi
(cd "$ROOT" && "$GO" run ./scripts/datagen locale "$src" "$dir")
outputs="$(for f in langid.json locales.json currency.json confusables.json params.json; do
	jq -n --arg k "$f" --arg v "$(sha256sum "$dir/$f" | cut -d' ' -f1)" '{($k): $v}'
done | jq -s 'add')"
jq -n --arg tag "$tag" --arg c "$commit" --arg cu "$cldr_url" --arg u "$udhr" --arg uu "$udhr_url" --arg uv "$uv" --arg su "$sec_url" \
	--argjson in "$inputs" --argjson out "$outputs" \
	'{cldr: {tag: $tag, commit: $c, url: $cu, license: "Unicode-3.0"},
	  udhr: {repository: "github.com/eric-muller/udhr", commit: $u, url: $uu, license: "translations: public texts of the OHCHR; markup: the UDHR in XML project"},
	  security: {version: $uv, url: $su, license: "Unicode-3.0"},
	  generator: "scripts/datagen (locale)", inputs: $in, outputs: $out}' >"$pin"
say "locale data from CLDR $tag ($commit), UDHR $udhr, Unicode security $uv"
