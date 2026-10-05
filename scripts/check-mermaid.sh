#!/usr/bin/env bash
# Usage: scripts/check-mermaid.sh FILE.md...
# Renders every Mermaid block (``` or ~~~ fences, indented or not) with
# mermaid-cli in a container, and fails on the first diagram that doesn't
# render, naming its file and position.
set -euo pipefail
image=${MERMAID_IMAGE:-docker.io/minlag/mermaid-cli:latest}
engine=$(command -v docker || command -v podman)
work=$(mktemp -d); chmod 777 "$work"; trap 'rm -rf "$work"' EXIT
n=0
for md in "$@"; do
  if [ ! -f "$md" ]; then
    echo "::error::$md: no such file"; exit 1
  fi
  # Split the file's mermaid blocks into numbered .mmd files.
  n=$((n + 1))
  awk -v out="$work" -v id="$n" '
    { sub(/\r$/, "") }
    !inblock && /^[[:space:]]*(```|~~~)[[:space:]]*mermaid[[:space:]]*$/ { k++; f = sprintf("%s/%s.%03d.mmd", out, id, k); inblock = 1; next }
    inblock && /^[[:space:]]*(```|~~~)[[:space:]]*$/ { inblock = 0; close(f); next }
    inblock { print > f }' "$md"
  printf '%s\t%s\n' "$n" "$md" >> "$work/index"
done
count=0
for mmd in "$work"/*.mmd; do
  [ -e "$mmd" ] || break
  count=$((count + 1))
  name=$(basename "$mmd")
  file=$(awk -F'\t' -v id="${name%%.*}" '$1 == id { print $2 }' "$work/index")
  where="$file, diagram $((10#$(basename "$name" .mmd | cut -d. -f2)))"
  if ! "$engine" run --rm -v "$work:/data" "$image" -i "/data/$name" -o "/data/$name.svg" >"$work/log" 2>&1; then
    echo "::error::Mermaid diagram doesn't render: $where"
    grep -iE 'error|expect|parse' "$work/log" | head -10 || tail -20 "$work/log"
    exit 1
  fi
  echo "ok $where"
done
echo "$count diagrams render"
