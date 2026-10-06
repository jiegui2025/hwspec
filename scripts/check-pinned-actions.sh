#!/usr/bin/env bash
# Usage: scripts/check-pinned-actions.sh [WORKFLOW.yml...]
# Every workflow that holds something worth stealing (a write permission, a
# secret or a deployment environment) must pin each action by full commit
# SHA, and each docker:// action by digest: a moved tag can't change what
# runs with those privileges (owner, 2026-10-06: pinning required on
# critical changes). Workflows without privileges aren't checked.
# Defaults to every file in .github/workflows.
set -euo pipefail
cd "$(dirname "$0")/.."
[ "$#" -gt 0 ] || set -- .github/workflows/*.yml
fail=0
for wf in "$@"; do
  grep -qE '^[[:space:]]+[a-z-]+:[[:space:]]+write\b|secrets\.|^[[:space:]]+environment:' "$wf" || continue
  while IFS= read -r line; do
    n=${line%%:*}; ref=${line#*:}
    ref=$(sed -E 's/^[[:space:]]*-?[[:space:]]*uses:[[:space:]]*//; s/[[:space:]]+#.*$//; s/^["'\'']//; s/["'\'']$//' <<<"$ref")
    case $ref in
      ./*) continue ;;                                  # this repository's own workflow
      docker://*@sha256:*) continue ;;
      docker://*) ;;
      *@*) [[ ${ref##*@} =~ ^[0-9a-f]{40}$ ]] && continue ;;
    esac
    echo "::error file=$wf,line=$n::$ref is not pinned by full commit SHA (or digest), in a workflow with write permissions, secrets or an environment"
    fail=1
  done < <(grep -nE '^[[:space:]]*-?[[:space:]]*uses:' "$wf" || true)
done
[ "$fail" = 0 ] && echo "pinned actions: every privileged workflow pins by SHA"
exit "$fail"
