#!/usr/bin/env bash
# Usage: scripts/check-pinned-actions.sh [WORKFLOW.yml...]
# Every workflow that holds something worth stealing (a write permission, a
# secret or a deployment environment) must pin each action by full commit
# SHA, and each docker:// action by digest: a moved tag can't change what
# runs with those privileges (owner, 2026-10-06: pinning required on
# critical changes). Reusable workflows (workflow_call) and this
# repository's own actions (.github/actions) run with their caller's
# token, so they are always checked. Other workflows without privileges
# aren't. Defaults to every workflow and every local action.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" = 0 ]; then
  set -- .github/workflows/*.yml
  while IFS= read -r -d '' f; do set -- "$@" "$f"; done < <(find .github/actions -name 'action.y*ml' -print0 2>/dev/null)
fi
fail=0
for wf in "$@"; do
  case $wf in
    */.github/actions/*|.github/actions/*) ;;           # runs with its caller's token
    *) grep -qE '^[[:space:]]+[a-z-]+:[[:space:]]+write\b|secrets\.|^[[:space:]]+environment:|^[[:space:]]+workflow_call:' "$wf" || continue ;;
  esac
  while IFS= read -r line; do
    n=${line%%:*}; ref=${line#*:}
    ref=$(sed -E 's/^[[:space:]]*-?[[:space:]]*uses:[[:space:]]*//; s/[[:space:]]+#.*$//; s/^["'\'']//; s/["'\'']$//' <<<"$ref")
    case $ref in
      ./*) continue ;;                                  # this repository's own workflow
      docker://*@sha256:*) continue ;;
      docker://*) ;;
      *@*) [[ ${ref##*@} =~ ^[0-9a-f]{40}$ ]] && continue ;;
    esac
    echo "::error file=$wf,line=$n::$ref is not pinned by full commit SHA (or digest), in a workflow with write permissions, secrets or an environment, a reusable workflow or a local action"
    fail=1
  done < <(grep -nE '^[[:space:]]*-?[[:space:]]*uses:' "$wf" || true)
done
[ "$fail" = 0 ] && echo "pinned actions: every privileged or reusable workflow and local action pins by SHA"
exit "$fail"
