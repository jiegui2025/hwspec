#!/usr/bin/env bash
# Usage: scripts/coverage.sh COVERAGE.OUT MIN_PERCENT
# Prints a Markdown coverage summary and fails if total coverage is below MIN.
set -euo pipefail
profile=${1:?coverage profile}
min=${2:?minimum percent}

# With -coverpkg every test binary lists every block, so count each block
# once: covered if any test covered it. The table below counts the same way.
# The gate compares the unrounded figure, so 94.95% doesn't pass a 95% gate.
exact=$(awk 'NR > 1 { stmts[$1] = $2; if ($3 > 0) hit[$1] = 1 }
  END { for (b in stmts) { t += stmts[b]; if (b in hit) c += stmts[b] } printf "%.6f", 100 * c / t }' "$profile")
total=$(awk -v e="$exact" 'BEGIN { printf "%.2f", e }')

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

if awk -v t="$exact" -v m="$min" 'BEGIN { exit !(t + 0 < m + 0) }'; then
  echo
  echo "**Coverage ${total}% is below the required ${min}%.**"
  exit 1
fi
