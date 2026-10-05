#!/usr/bin/env bash
# Usage: scripts/coverage.sh COVERAGE.OUT MIN_PERCENT
# Prints a Markdown coverage summary and fails if total coverage is below MIN.
set -euo pipefail
profile=${1:?coverage profile}
min=${2:?minimum percent}

total=$(go tool cover -func="$profile" | awk '/^total:/ { sub("%", "", $3); print $3 }')

echo "### Test coverage: ${total}% (minimum ${min}%)"
echo
echo "| Package | Coverage |"
echo "|---|---|"
# Per-package coverage. With -coverpkg every test binary lists every block,
# so deduplicate: a block counts as covered if any test covered it.
awk 'NR > 1 {
  block = $1; stmts[block] = $2; if ($3 > 0) hit[block] = 1
} END {
  for (b in stmts) {
    pkg = b; sub(":.*", "", pkg); sub("/[^/]*$", "", pkg); sub("^github.com/jiegui2025/hwspec/?", "", pkg)
    total[pkg] += stmts[b]; if (b in hit) covered[pkg] += stmts[b]
  }
  for (p in total) printf "| %s | %.1f%% |\n", (p == "" ? "." : p), 100 * covered[p] / total[p]
}' "$profile" | sort

if awk -v t="$total" -v m="$min" 'BEGIN { exit !(t + 0 < m + 0) }'; then
  echo
  echo "**Coverage ${total}% is below the required ${min}%.**"
  exit 1
fi
