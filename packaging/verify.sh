#!/usr/bin/env bash
# Verify the packaging on Linux:
#
#   packaging/verify.sh
#
# It builds both binaries, checks that the version is the same in both and in
# the installer script, builds the Linux tarball, installs it twice into a
# temporary XDG prefix, checks idempotence and the printed uninstall command,
# and proves that uninstalling keeps the data directory. The Inno Setup script
# is checked statically: Inno Setup itself is Windows-only and is not compiled
# here.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO="${GO:-go}"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/mailsense-packaging.XXXXXX")"
trap 'rm -rf -- "$tmp"' EXIT

pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

say "build both binaries"
(cd "$ROOT" && scripts/build.sh all)

version="$(tr -d '\r\n' <"$ROOT/dist/VERSION")"
[[ -n "$version" ]] || fail "dist/VERSION is empty"
grep -a -F -q -- "$version" "$ROOT/dist/mailsense" || fail "the Linux binary does not embed $version"
grep -a -F -q -- "$version" "$ROOT/dist/mailsense.exe" || fail "the Windows binary does not embed $version"
pass "both binaries embed $version"

say "check the Windows installer statically"
iss="$ROOT/packaging/windows/mailsense.iss"
[[ -f "$iss" ]] || fail "packaging/windows/mailsense.iss is missing"

grep -Fq 'dist\VERSION' "$iss" || fail "the installer does not read dist\\VERSION"
grep -Fq 'SameStr(AppVersion, DistVersion)' "$iss" || fail "the installer does not reject a mismatched /DAppVersion"
grep -Fq 'OutputBaseFilename=mailsense-{#AppVersion}-setup' "$iss" || fail "the installer name is not derived from AppVersion"
grep -Fq 'VersionInfoProductTextVersion={#AppVersion}' "$iss" || fail "the installer version stamp is not derived from AppVersion"
grep -Fq 'AppId={{6E2E7E2B-7C1F-4C55-9E4B-2F7A6C1D9A31}' "$iss" || fail "the stable AppId changed"
grep -Fq 'PrivilegesRequired=lowest' "$iss" || fail "the installer is not per-user"
grep -Fq 'DefaultDirName={localappdata}\Programs\Mailsense' "$iss" || fail "the install directory is not per-user LocalAppData"
grep -Fq 'Name: "{group}\Mailsense"' "$iss" || fail "the Start menu shortcut is missing"
grep -Fq 'Name: "{autodesktop}\Mailsense"' "$iss" || fail "the desktop shortcut is missing"
grep -Fq 'Tasks: desktopicon' "$iss" || fail "the desktop shortcut is not optional"
grep -Fq 'Flags: unchecked' "$iss" || fail "the desktop shortcut is not unchecked by default"
grep -Fq 'postinstall' "$iss" || fail "the installer does not offer to launch Mailsense"
grep -Fq 'FileExists(SourceExe)' "$iss" || fail "the payload binary is not guarded"
grep -Fq 'FileExists(LicenseFile)' "$iss" || fail "LICENSE is not guarded"
grep -Fq 'FileExists(NoticeFile)' "$iss" || fail "NOTICE is not guarded"
grep -Fq 'downloaded once on first use' "$iss" || fail "the installer does not explain the first-use runtime download"
grep -Fq 'first-run setup opens' "$iss" || fail "the installer does not mention the first-run setup"
grep -Fq 'CurUninstallStepChanged' "$iss" || fail "the uninstall does not explain that data is kept"
grep -Fq '{localappdata}\Mailsense' "$iss" || fail "the uninstall message does not name the data directory"
grep -Fq 'remain in ' "$iss" || fail "the uninstall message does not say the data remains"
if grep -Eq '^\[UninstallDelete\]' "$iss"; then
	fail "an [UninstallDelete] section could delete user data"
fi

grep -Fq 'go run ./cmd/gensite -pages=en' "$iss" || fail "the English docs list is not generated"
grep -Fq 'go run ./cmd/gensite -pages=fr' "$iss" || fail "the French docs list is not generated"
grep -Fq 'docs-pages.iss' "$iss" || fail "the generated docs include is missing"
grep -Fq '#include PagesInclude' "$iss" || fail "the generated docs include is not used"
if grep -Eq '^[[:space:]]*Source:.*docs[/\\].*\.md' "$iss"; then
	fail "a docs page is hand-listed in the installer"
fi

while IFS= read -r src; do
	case "$src" in
	'{#SourceExe}') file="$ROOT/dist/mailsense.exe" ;;
	'{#LicenseFile}') file="$ROOT/LICENSE" ;;
	'{#NoticeFile}') file="$ROOT/NOTICE" ;;
	*) file="$ROOT/packaging/windows/${src//\\//}" ;;
	esac
	[[ -s "$file" ]] || fail "the installer references a missing source: $src"
done < <(sed -n 's/^[[:space:]]*Source:[[:space:]]*"\([^"]*\)".*/\1/p' "$iss")

icon="$(sed -n 's/^SetupIconFile=//p' "$iss")"
[[ -s "$ROOT/packaging/windows/${icon//\\//}" ]] || fail "the installer icon is missing: $icon"

for lang in en fr; do
	mapfile -t pages < <(cd "$ROOT" && "$GO" run ./cmd/gensite -pages="$lang")
	((${#pages[@]})) || fail "cmd/gensite returned no $lang pages"
	for page in "${pages[@]}"; do
		[[ -s "$ROOT/$page" ]] || fail "the generated $lang page is missing: $page"
	done
done
pass "installer sources, version, shortcuts, uninstall message and generated docs are consistent"

say "check the Linux tarball and installer"
for script in "$ROOT/packaging/linux/install.sh" "$ROOT/packaging/linux/package.sh"; do
	bash -n "$script" || fail "syntax error in $script"
done

tarball="$("$ROOT/packaging/linux/package.sh")"
[[ -s "$tarball" ]] || fail "the package script did not produce a tarball"
tar -xzf "$tarball" -C "$tmp"
tar -tzf "$tarball" >"$tmp/tarball.list"
name="mailsense-${version}-linux-amd64"
[[ "$(basename "$tarball")" == "${name}.tar.gz" ]] || fail "the tarball name is not derived from $version"
for item in mailsense install.sh README.md LICENSE NOTICE VERSION SHA256SUMS; do
	grep -Fxq "$name/$item" "$tmp/tarball.list" || fail "the tarball is missing $item"
done
pass "tarball ${name}.tar.gz has the expected name and payload"
src="$tmp/$name"
home="$tmp/home with space"
data="$tmp/xdg data"
mkdir -p "$home" "$data"

run_installer() { # log
	env HOME="$home" XDG_DATA_HOME="$data" XDG_BIN_HOME= "$src/install.sh" >"$1"
}
run_installer "$tmp/install-1.log"
bin="$home/.local/bin/mailsense"
entry="$data/applications/mailsense.desktop"
[[ -x "$bin" ]] || fail "the installer did not install the binary under the XDG bin directory"
[[ -f "$entry" ]] || fail "the installer did not create the desktop entry"
grep -Fxq "Exec=\"$bin\"" "$entry" || fail "the desktop entry does not point at the installed binary"
grep -Fq 'mailsense: OK' "$tmp/install-1.log" || fail "the installer did not verify the shipped checksum"

before="$(sha256sum "$bin" "$entry")"
run_installer "$tmp/install-2.log"
after="$(sha256sum "$bin" "$entry")"
[[ "$before" == "$after" ]] || fail "a second install changed the installed files"
pass "install is idempotent and the desktop entry points at $bin"

mkdir -p "$data/mailsense"
printf 'keep\n' >"$data/mailsense/keep.txt"
uninstall_cmd="$(sed -n 's/^uninstall: //p' "$tmp/install-2.log")"
[[ -n "$uninstall_cmd" ]] || fail "the installer did not print an uninstall command"
bash -c "$uninstall_cmd"
[[ ! -e "$bin" ]] || fail "the printed uninstall command left the binary behind"
[[ ! -e "$entry" ]] || fail "the printed uninstall command left the desktop entry behind"
[[ -f "$data/mailsense/keep.txt" ]] || fail "the printed uninstall command deleted the data directory"
pass "the printed uninstall command removes the install and keeps the data"

run_installer "$tmp/install-3.log"
env HOME="$home" XDG_DATA_HOME="$data" XDG_BIN_HOME= "$src/install.sh" --uninstall >"$tmp/uninstall.log"
[[ ! -e "$bin" && ! -e "$entry" ]] || fail "--uninstall left the install behind"
[[ -f "$data/mailsense/keep.txt" ]] || fail "--uninstall deleted the data directory"
pass "--uninstall is idempotent and keeps the data"

bad="$tmp/bad package"
cp -a "$src" "$bad"
printf 'x' >>"$bad/mailsense"
if env HOME="$home" XDG_DATA_HOME="$data" XDG_BIN_HOME= "$bad/install.sh" >"$tmp/bad.log" 2>&1; then
	fail "a tampered binary was installed"
fi
grep -Fq 'checksum mismatch' "$tmp/bad.log" || fail "a tampered binary failed without the checksum message"
pass "a tampered binary is refused"

printf '\npackaging verification passed\n'
printf 'note: Inno Setup is not available on Linux; packaging/windows/mailsense.iss was checked statically and not compiled.\n'
