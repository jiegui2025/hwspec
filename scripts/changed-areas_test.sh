#!/usr/bin/env bash
# Usage: scripts/changed-areas_test.sh
# Checks which parts of CI scripts/changed-areas.sh selects for typical
# changes. That script decides which required jobs run, so a wrong pattern
# silently skips checks.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
# changed files (comma-separated) | expected go nix workflows docs vm
while IFS='|' read -r files want; do
	got=$(tr ',' '\n' <<<"$files" | sed '/^$/d' | scripts/changed-areas.sh --files |
		sed 's/.*=//' | paste -sd' ')
	if [ "$got" != "$want" ]; then
		echo "FAIL [$files]: got '$got', want '$want'"
		fail=1
	fi
done <<'CASES'
cmd/hwspec/main.go|true true false false true
internal/collect/testdata/machines/hp-elitedesk-800-g5-mini/root/proc/cpuinfo|true true false false true
go.sum|true true false false true
vendor/example.com/x/x.go|true true false false true
.golangci.yml|true true false false true
Makefile|true true false false true
scripts/coverage.sh|true true false false true
flake.lock|false true false false false
flake.nix|false true false false false
.github/workflows/ci.yml|true true true true true
.github/actionlint.yaml|true true true true true
scripts/changed-areas.sh|true true true true true
scripts/changed-areas_test.sh|true true true true true
scripts/vm-run.sh|false false false false true
scripts/vm-check.sh|false false false false true
scripts/vm-seed.sh|false false false false true
.github/workflows/vms.yml|true true true true true
scripts/vm-run.sh.orig|false false false false false
README.md|false false false true false
docs/adr/0001-static-go-cli.md,CONTRIBUTING.md|false false false true false
README.md,cmd/hwspec/main.go|true true false true true
LICENSE|false false false false false
|false false false false false
CASES

if [ "$fail" = 0 ]; then echo "changed-areas: all cases pass"; fi
exit "$fail"
