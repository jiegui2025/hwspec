#!/usr/bin/env bash
# Usage: scripts/changed-diagrams.sh BASE HEAD
# Prints, NUL-separated, the Markdown files whose change between BASE and
# HEAD touches a Mermaid block (a fence or a line inside one), so CI renders
# only diagrams a change could break (#189). Prose edits print nothing.
set -euo pipefail
base=${1:?base}; head=${2:?head}
git diff -z --no-renames --name-only --diff-filter=d "$base...$head" -- '*.md' |
while IFS= read -r -d '' md; do
  # Lines (1-based) of every Mermaid block in the new version: "start end".
  blocks=$(git show "$head:$md" | awk '
    { sub(/\r$/, "") }
    !inblock && /^[[:space:]]*(```|~~~)[[:space:]]*mermaid[[:space:]]*$/ { start = NR; inblock = 1; next }
    inblock && /^[[:space:]]*(```|~~~)[[:space:]]*$/ { print start, NR; inblock = 0 }
    END { if (inblock) print start, NR }')
  [ -n "$blocks" ] || continue
  # New-side line ranges of each hunk; a pure deletion (count 0) sits
  # between line c and c+1.
  hunks=$(git diff -U0 --no-renames "$base...$head" -- "$md" |
    sed -nE 's/^@@ -[0-9,]+ \+([0-9]+)(,([0-9]+))? @@.*/\1 \3/p' |
    awk '{ c = $1; n = ($2 == "") ? 1 : $2; if (n == 0) print c, c + 1; else print c, c + n - 1 }')
  if awk -v blocks="$blocks" '
      BEGIN { nb = split(blocks, b, "\n") }
      { for (i = 1; i <= nb; i++) { split(b[i], r, " "); if ($1 <= r[2] && $2 >= r[1]) { found = 1; exit } } }
      END { exit !found }' <<<"$hunks"; then
    printf '%s\0' "$md"
  fi
done
