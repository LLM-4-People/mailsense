#!/usr/bin/env bash
# Scan every tracked file for personal data and environment residue.
#
# The deny and allow lists live in scripts/pii-patterns.tsv. This script holds
# no pattern of its own, so the list has one owner and cannot drift.
#
#   scripts/check-pii.sh              scan the tracked tree
#   scripts/check-pii.sh --self-test  prove every pattern and allow term
#   scripts/check-pii.sh --help       show this text
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PATTERNS="$ROOT/scripts/pii-patterns.tsv"
PATTERNS_REL="scripts/pii-patterns.tsv"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
usage: scripts/check-pii.sh [--self-test|--help]

  no argument   scan every tracked file for residue
  --self-test   check every deny pattern and every allow term
  --help        show this text
EOF
}

declare -a deny_category=() deny_pattern=() deny_sample=()
declare -a allow_category=() allow_pattern=() allow_sample=()

# load_patterns reads the one machine-readable list and validates its shape.
load_patterns() {
	[[ -f "$PATTERNS" ]] || die "missing pattern file: $PATTERNS_REL"
	local line_no=0 kind category pattern sample _note key
	declare -A seen=()
	while IFS=$'\t' read -r kind category pattern sample _note; do
		line_no=$((line_no + 1))
		[[ -z "$kind" || "$kind" == \#* ]] && continue
		[[ -n "$category" && -n "$pattern" && -n "$sample" && -n "$_note" ]] ||
			die "$PATTERNS_REL:$line_no: expected kind, category, pattern, sample and note"
		key="$kind|$pattern"
		[[ -z "${seen[$key]:-}" ]] || die "$PATTERNS_REL:$line_no: duplicate $kind pattern: $pattern"
		seen[$key]=1
		case "$kind" in
		deny)
			deny_category+=("$category")
			deny_pattern+=("$pattern")
			deny_sample+=("$sample")
			;;
		allow)
			[[ "$pattern" == "$sample" ]] ||
				die "$PATTERNS_REL:$line_no: an allow pattern is a literal term and must equal its sample"
			allow_category+=("$category")
			allow_pattern+=("$pattern")
			allow_sample+=("$sample")
			;;
		*)
			die "$PATTERNS_REL:$line_no: kind must be deny or allow, got: $kind"
			;;
		esac
	done <"$PATTERNS"
	((${#deny_pattern[@]})) || die "$PATTERNS_REL: no deny pattern"
	((${#allow_pattern[@]})) || die "$PATTERNS_REL: no allow term"
}

# self_test proves the list is live: each deny pattern matches its sample, no
# deny pattern rejects an allowed term, and removing one deny pattern removes
# the match for its sample.
self_test() {
	local i j matched
	shopt -s nocasematch
	for i in "${!deny_pattern[@]}"; do
		if [[ ! "${deny_sample[$i]}" =~ ${deny_pattern[$i]} ]]; then
			die "self-test: ${deny_category[$i]} pattern does not match its sample: ${deny_pattern[$i]}"
		fi
	done
	for i in "${!allow_pattern[@]}"; do
		for j in "${!deny_pattern[@]}"; do
			if [[ "${allow_sample[$i]}" =~ ${deny_pattern[$j]} ]]; then
				die "self-test: ${deny_category[$j]} pattern rejects the allowed ${allow_category[$i]}: ${allow_sample[$i]}"
			fi
		done
	done
	for i in "${!deny_pattern[@]}"; do
		matched=0
		for j in "${!deny_pattern[@]}"; do
			((j == i)) && continue
			if [[ "${deny_sample[$i]}" =~ ${deny_pattern[$j]} ]]; then
				matched=1
				break
			fi
		done
		if ((matched)); then
			die "self-test: pattern is redundant or too broad: ${deny_pattern[$i]}"
		fi
	done
	printf 'PII self-test passed: %d deny patterns, %d allow terms\n' \
		"${#deny_pattern[@]}" "${#allow_pattern[@]}"
}

# scan prints every residue hit as file:line: category: matched stem and exits
# nonzero when any hit exists.
scan() {
	local -a files=()
	local file
	while IFS= read -r file; do
		[[ "$file" == "$PATTERNS_REL" ]] && continue
		files+=("$file")
	done < <(git -C "$ROOT" ls-files)
	((${#files[@]})) || die "no tracked files found under $ROOT"

	local hits=0 i record path rest line match
	for i in "${!deny_pattern[@]}"; do
		while IFS= read -r record; do
			[[ -n "$record" ]] || continue
			path="${record%%:*}"
			rest="${record#*:}"
			line="${rest%%:*}"
			match="${rest#*:}"
			printf '%s:%s: %s: %s\n' "$path" "$line" "${deny_category[$i]}" "$match"
			hits=$((hits + 1))
		done < <(grep -HnoiE -I -e "${deny_pattern[$i]}" -- "${files[@]}" 2>/dev/null || true)
	done
	if ((hits > 0)); then
		printf 'error: %d PII residue matches in tracked files\n' "$hits" >&2
		printf '       rerun with: bash scripts/check-pii.sh\n' >&2
		return 1
	fi
	printf 'PII check passed: %d tracked files, %d deny patterns\n' \
		"${#files[@]}" "${#deny_pattern[@]}"
}

load_patterns
case "${1:-}" in
--self-test)
	self_test
	;;
--help | -h)
	usage
	;;
"")
	scan
	;;
*)
	usage >&2
	exit 2
	;;
esac
