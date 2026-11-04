#!/usr/bin/env bash
# Per-user Linux installer for a Mailsense tarball.
#
#   ./install.sh [--prefix DIR]
#
# Installs the binary to ~/.local/bin, a desktop entry for the current user,
# and leaves the database in ${XDG_DATA_HOME:-~/.local/share}/mailsense.
set -euo pipefail

prefix="${HOME}/.local"
while [[ $# -gt 0 ]]; do
	case "$1" in
	--prefix) prefix="${2:?--prefix needs a directory}"; shift 2 ;;
	-h | --help) sed -n '2,8p' "$0"; exit 0 ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
binary="${here}/mailsense"
[[ -x "$binary" ]] || { echo "mailsense binary not found beside install.sh" >&2; exit 1; }

install -d "${prefix}/bin" "${prefix}/share/applications"
install -m 0755 "$binary" "${prefix}/bin/mailsense"

cat > "${prefix}/share/applications/mailsense.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Mailsense
Comment=Local-first mail triage that explains every decision
Exec=${prefix}/bin/mailsense
Terminal=false
Categories=Office;Email;
DESKTOP

echo "installed ${prefix}/bin/mailsense"
echo "data directory: ${XDG_DATA_HOME:-$HOME/.local/share}/mailsense"
