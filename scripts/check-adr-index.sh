#!/usr/bin/env bash
# Usage: scripts/check-adr-index.sh [ADR-DIR]
# Every decision record (docs/adr/NNNN-*.md) is listed in the index
# (docs/adr/README.md), and every record the index links to exists: a
# design decision nobody can find isn't recorded.
set -euo pipefail
dir=${1:-docs/adr}
index=$dir/README.md
[ -f "$index" ] || { echo "no index at $index"; exit 1; }
fail=0
for f in "$dir"/[0-9][0-9][0-9][0-9]-*.md; do
	[ -e "$f" ] || continue
	name=${f##*/}
	# Linked under its own number: "[0003](0003-capture-format.md)".
	grep -qF "[${name:0:4}]($name)" "$index" || { echo "$index doesn't link $name as [${name:0:4}]"; fail=1; }
done
# One record per number.
for n in $(for f in "$dir"/[0-9][0-9][0-9][0-9]-*.md; do [ -e "$f" ] && basename "$f" | cut -c1-4; done | sort | uniq -d); do
	echo "more than one record numbered $n: $(cd "$dir" && echo "$n"-*.md)"
	fail=1
done
while read -r name; do
	[ -f "$dir/$name" ] || { echo "$index links $name, which doesn't exist"; fail=1; }
done < <(grep -oE '\]\([0-9]{4}-[^)]+\.md\)' "$index" | sed 's/^](//; s/)$//')
[ "$fail" = 0 ] && echo "ADR index: complete"
exit "$fail"
