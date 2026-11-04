#!/usr/bin/env bash
# Seal the TEST split of a gold sample (internal/goldset.Seal): every line
# and value naming a test message, in every file of the directory, moves
# into <dir>/sealed/ (mode 000); the kept files name train and dev
# messages only. Prints counts only (never a label or an input). Running
# it again seals only what was added since (a new judge file). The test
# split is then read once, by goldset.OpenTest (GOLD_UNSEAL=<model digest>,
# a ledger refuses a second time).
#
#   scripts/gold-seal.sh [gold dir]   (default: the newest ~/.local/share/mailsense/bench/sample-*)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
dir="${1:-$(gold_dir)}"
[[ -n "$dir" && -f "$dir/splits.json" ]] || die "no gold sample (splits.json) at '${dir}'"
say "sealing the test split of $dir"
cd "$ROOT"
"$GO" run ./scripts/goldseal "$dir"
