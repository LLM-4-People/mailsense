#!/usr/bin/env bash
# Per-user Linux installer for a Mailsense tarball.
#
#   ./install.sh [--prefix DIR]
#   ./install.sh --uninstall
#
# The default install uses the current user's XDG locations: the binary goes to
# $XDG_BIN_HOME or ~/.local/bin, and the menu entry to
# $XDG_DATA_HOME/applications. The database stays in
# ${MAILSENSE_DATA_DIR:-${XDG_DATA_HOME:-~/.local/share}/mailsense}; installing
# and uninstalling never touch it.
#
# A SHA256SUMS file beside install.sh is verified when present. The installer
# is idempotent: running it twice replaces the same two files and changes
# nothing else.
set -euo pipefail

usage() { sed -n '2,12p' "$0"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

prefix=""
uninstall=0
while [[ $# -gt 0 ]]; do
	case "$1" in
	--prefix) prefix="${2:?--prefix needs a directory}"; shift 2 ;;
	--uninstall) uninstall=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown argument: $1 (try --help)" ;;
	esac
done

home="${HOME:?HOME is not set}"
data_home="${home}/.local/share"
if [[ "${XDG_DATA_HOME:-}" == /* ]]; then data_home="$XDG_DATA_HOME"; fi
bin_home="${home}/.local/bin"
if [[ "${XDG_BIN_HOME:-}" == /* ]]; then bin_home="$XDG_BIN_HOME"; fi
if [[ -n "$prefix" ]]; then
	bin_dir="${prefix}/bin"
	apps_dir="${prefix}/share/applications"
else
	bin_dir="${bin_home}"
	apps_dir="${data_home}/applications"
fi
binary="${bin_dir}/mailsense"
entry="${apps_dir}/mailsense.desktop"
if [[ -n "${MAILSENSE_DATA_DIR:-}" ]]; then
	data_dir="${MAILSENSE_DATA_DIR}"
else
	data_dir="${data_home}/mailsense"
fi

if ((uninstall)); then
	rm -f -- "$binary" "$entry"
	printf 'removed %s\n' "$binary"
	printf 'removed %s\n' "$entry"
	printf 'data kept in %s\n' "$data_dir"
	exit 0
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_binary="${here}/mailsense"
[[ -x "$source_binary" ]] || die "mailsense binary not found beside install.sh"

if [[ -f "${here}/SHA256SUMS" ]]; then
	matches="$(awk '$2 == "mailsense" || $2 == "*mailsense" { print }' "${here}/SHA256SUMS")"
	[[ -n "$matches" ]] || die "SHA256SUMS has no entry for mailsense"
	(cd "$here" && printf '%s\n' "$matches" | sha256sum -c -) || die "mailsense checksum mismatch"
fi

# The desktop entry quotes Exec, so a path with spaces is valid. The other
# characters would need escaping inside that quoted field; refuse them rather
# than write an entry that silently runs a different command.
case "$binary" in
*'"'* | *'`'* | *'$'* | *\\* | *$'\n'* | *$'\t'*) die "the install path contains a character that cannot be written safely into a desktop entry: $binary" ;;
esac

install -d -- "$bin_dir" "$apps_dir"
install -m 0755 -- "$source_binary" "$binary"

tmp_entry="$(mktemp "${entry}.XXXXXX")"
trap 'rm -f -- "$tmp_entry"' EXIT
cat >"$tmp_entry" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Mailsense
Comment=Local-first mail triage that explains every decision
Exec="${binary}"
Terminal=false
Categories=Office;Email;
DESKTOP
chmod 0644 "$tmp_entry"
mv -f -- "$tmp_entry" "$entry"
trap - EXIT

version=""
if [[ -s "${here}/VERSION" ]]; then
	version=" $(tr -d '\r\n' <"${here}/VERSION")"
fi
printf 'installed %s%s\n' "$binary" "$version"
printf 'desktop entry %s\n' "$entry"
printf 'data directory %s (kept on uninstall)\n' "$data_dir"
printf 'uninstall: rm -f -- %q %q\n' "$binary" "$entry"
