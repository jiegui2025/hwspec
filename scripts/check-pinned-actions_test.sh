#!/usr/bin/env bash
# Usage: scripts/check-pinned-actions_test.sh
# check-pinned-actions.sh must refuse a tag, a branch or a short SHA in a
# privileged workflow, and accept full SHAs, local workflows and
# unprivileged workflows, or it checks nothing.
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/check-pinned-actions.sh
dir=$(mktemp -d); trap 'rm -rf "$dir"' EXIT
sha=3d3c42e5aac5ba805825da76410c181273ba90b1
fail=0
# expect pass|fail NAME YAML
expect() {
  local want=$1 name=$2 code=0
  printf '%s\n' "$3" > "$dir/$name.yml"
  "$script" "$dir/$name.yml" >/dev/null 2>&1 || code=$?
  if { [ "$want" = pass ] && [ "$code" != 0 ]; } || { [ "$want" = fail ] && [ "$code" = 0 ]; }; then
    echo "FAIL $name: want $want, exit $code"
    fail=1
  fi
}
priv='jobs:
  x:
    permissions:
      contents: write
    steps:'
expect pass pinned "$priv
      - uses: actions/checkout@$sha # v7"
expect fail tag "$priv
      - uses: actions/checkout@v7"
expect fail short "$priv
      - uses: actions/checkout@3d3c42e"
expect fail branch "$priv
      - uses: \"actions/checkout@main\""
expect pass local "$priv
      - uses: ./.github/workflows/vms.yml"
expect fail docker "$priv
      - uses: docker://alpine:3.20"
expect pass docker-digest "$priv
      - uses: docker://alpine@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
# shellcheck disable=SC2016 # ${{ }} is GitHub expression text, not shell
expect fail secret 'jobs:
  x:
    steps:
      - uses: some/action@v1
        env:
          K: ${{ secrets.KEY }}'
expect fail environment 'jobs:
  x:
    environment: production
    steps:
      - uses: some/action@v1'
expect pass unprivileged 'jobs:
  x:
    permissions:
      contents: read
    steps:
      - uses: some/action@v1'
[ "$fail" = 0 ] && echo "check-pinned-actions: all cases pass"
exit "$fail"
