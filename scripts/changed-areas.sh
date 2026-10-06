#!/usr/bin/env bash
# Usage: scripts/changed-areas.sh BASE HEAD
#        scripts/changed-areas.sh --files < list   (one path per line; tests)
# Prints key=true|false lines (for $GITHUB_OUTPUT) saying which parts of
# CI a change between BASE and HEAD needs:
#   go         Go code, modules, embedded data, fixtures, the capture schema,
#              lint/build config
#   nix        the flake, or anything that changes what it builds
#   workflows  CI itself: then everything runs, plus actionlint
#   docs       Markdown (Mermaid diagrams are rendered to check them)
#   vm         Go code or the VM harness (scripts/vm-*.sh, vms.yml): boot the VMs
#   verify     release verification (verify.yml, scripts/verify-release*.sh):
#              test that it catches tampering, against the live edge release
set -euo pipefail
if [ "${1:-}" = --files ]; then
	files=$(cat)
else
	base=${1:?base}; head=${2:?head}
	# Three dots: changes since the branch point, not differences from a
	# base that has moved on since.
	files=$(git diff --no-renames --name-only "$base...$head") # a rename lists both paths
fi
match() { grep -Eq "$1" <<<"$files"; }

go=false; nix=false; workflows=false; docs=false; vm=false; verify=false
match '(\.go$|^go\.(mod|sum|work|work\.sum)$|^vendor/|^(cmd|internal|tools|schema)/|^\.golangci\.ya?ml$|^Makefile$|^scripts/coverage\.sh$)' && go=true
match '^flake\.(nix|lock)$' && nix=true
match '^(\.github/(workflows|actions)/|\.github/actionlint\.ya?ml$|scripts/(changed-areas(_test)?|check-adr-index|check-commits(_test)?|check-mermaid|verify-release(_test)?)\.sh$)' && workflows=true
match '\.md$' && docs=true
match '^(scripts/vm-[a-z-]+\.sh|\.github/workflows/vms\.yml)$' && vm=true
match '^(scripts/verify-release(_test)?\.sh|\.github/workflows/verify\.yml)$' && verify=true

# The flake builds the Go code, and the VMs check what it captures;
# changing CI re-validates everything.
if $go; then nix=true; vm=true; fi
if $workflows; then go=true; nix=true; docs=true; vm=true; fi

printf 'go=%s\nnix=%s\nworkflows=%s\ndocs=%s\nvm=%s\nverify=%s\n' "$go" "$nix" "$workflows" "$docs" "$vm" "$verify"
