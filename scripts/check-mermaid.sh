#!/usr/bin/env bash
# Usage: scripts/check-mermaid.sh FILE.md...
# Renders every Mermaid block (``` or ~~~ fences, indented or not) with
# mermaid-cli in a container, all in one call; when that fails, renders them
# one at a time to name the first diagram that doesn't render (its file and
# position).
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
mmds=("$work"/*.mmd)
[ -e "${mmds[0]}" ] || mmds=()
# A broken extraction must not pass as "0 diagrams render": the number of
# blocks found must be the number of opening fences in the files.
fences=$(cat "$@" | tr -d '\r' | grep -cE '^[[:space:]]*(```|~~~)[[:space:]]*mermaid[[:space:]]*$' || true)
if [ "$fences" != "${#mmds[@]}" ]; then
  echo "::error::found $fences Mermaid fences but extracted ${#mmds[@]} diagrams"
  exit 1
fi
if [ "${#mmds[@]}" = 0 ]; then
  echo "0 diagrams render"
  exit 0
fi
where() {
  local name file
  name=$(basename "$1")
  file=$(awk -F'\t' -v id="${name%%.*}" '$1 == id { print $2 }' "$work/index")
  echo "$file, diagram $((10#$(basename "$name" .mmd | cut -d. -f2)))"
}

# One mmdc call renders every diagram in one browser: each separate call
# starts Chromium, which is what a diagram cost (29 diagrams took 70 s one
# by one, 67 s in one container, 27 s in one call; #175).
for mmd in "${mmds[@]}"; do
  printf '```mermaid\n'
  cat "$mmd"
  printf '```\n\n'
done >"$work/all.md"
if "$engine" run --rm -v "$work:/data" "$image" -i /data/all.md -o /data/all.out.md >"$work/log" 2>&1; then
  for mmd in "${mmds[@]}"; do
    echo "ok $(where "$mmd")"
  done
  echo "${#mmds[@]} diagrams render"
  exit 0
fi

# Something doesn't render: find which, one diagram at a time.
for mmd in "${mmds[@]}"; do
  name=$(basename "$mmd")
  if ! "$engine" run --rm -v "$work:/data" "$image" -i "/data/$name" -o "/data/$name.svg" >"$work/log1" 2>&1; then
    echo "::error::Mermaid diagram doesn't render: $(where "$mmd")"
    grep -iE 'error|expect|parse' "$work/log1" | head -10 || tail -20 "$work/log1"
    exit 1
  fi
done
echo "::error::Mermaid diagrams render one by one but not together"
grep -iE 'error|expect|parse' "$work/log" | head -10 || tail -20 "$work/log"
exit 1
