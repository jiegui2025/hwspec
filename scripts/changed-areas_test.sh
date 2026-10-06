#!/usr/bin/env bash
# Usage: scripts/changed-areas_test.sh
# Checks which parts of CI scripts/changed-areas.sh selects for typical
# changes. That script decides which required jobs run, so a wrong pattern
# silently skips checks.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
# changed files (comma-separated) | expected go nix workflows actionlint docs vm verify
while IFS='|' read -r files want; do
	got=$(tr ',' '\n' <<<"$files" | sed '/^$/d' | scripts/changed-areas.sh --files |
		sed 's/.*=//' | paste -sd' ')
	if [ "$got" != "$want" ]; then
		echo "FAIL [$files]: got '$got', want '$want'"
		fail=1
	fi
done <<'CASES'
cmd/hwspec/main.go|true true false false false true false
cmd/hwspec/cli_test.go|true true false false false false false
internal/trust/trust.go|true true false false false true false
internal/collect/system.go|true true false false false true false
internal/collect/pci.go|true true false false false true false
internal/collect/sysfs.go|true true false false false true false
internal/collect/devices.go|true true false false false false false
internal/collect/system_test.go|true true false false false false false
internal/advisor/checks.go|true true false false false false false
internal/ids/ids.go|true true false false false false false
internal/collect/testdata/machines/hp-elitedesk-800-g5-mini/root/proc/cpuinfo|true true false false false false false
go.sum|true true false false false true false
go.mod|true true false false false true false
vendor/example.com/x/x.go|true true false false false true false
.golangci.yml|true true false false false false false
Makefile|true true false false false false false
scripts/coverage.sh|true true false true false false false
schema/capture-v1.json|true true false false false false false
kb/rules/needs-attention.yaml|true true false false false false false
kb/README.md|false false false false true false false
flake.lock|false true false false false false false
flake.nix|false true false false false false false
.github/workflows/ci.yml|true true true true true true false
.github/actions/setup/action.yml|true true true true true true false
scripts/changed-areas.sh|true true true true true true false
scripts/changed-areas_test.sh|true true true true true true false
.github/actionlint.yaml|false false false true false false false
.github/workflows/release.yml|false false false true false false false
.github/workflows/edge.yml|false false false true false false false
.github/workflows/ids.yml|false false false true false false false
.github/workflows/canary.yml|false false false true false false false
scripts/check-commits.sh|false false false true false false false
scripts/check-commits_test.sh|false false false true false false false
scripts/check-adr-index.sh|false false false true true false false
scripts/check-mermaid.sh|false false false true true false false
scripts/changed-diagrams.sh|false false false true true false false
scripts/changed-diagrams_test.sh|false false false true true false false
scripts/verify-release.sh|false false false true false false true
scripts/verify-release_test.sh|false false false true false false true
.github/workflows/verify.yml|false false false true false false true
scripts/vm-run.sh|false false false true false true false
scripts/vm-check.sh|false false false true false true false
scripts/vm-seed.sh|false false false true false true false
.github/workflows/vms.yml|false false false true false true false
scripts/vm-run.sh.orig|false false false false false false false
README.md|false false false false true false false
docs/adr/0009-new-decision.md|false false false false true false false
docs/adr/0001-static-go-cli.md,CONTRIBUTING.md|false false false false true false false
README.md,cmd/hwspec/main.go|true true false false true true false
README.md,internal/advisor/checks.go|true true false false true false false
LICENSE|false false false false false false false
|false false false false false false false
CASES

if [ "$fail" = 0 ]; then echo "changed-areas: all cases pass"; fi
exit "$fail"
