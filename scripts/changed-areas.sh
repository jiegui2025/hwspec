#!/usr/bin/env bash
# Usage: scripts/changed-areas.sh BASE HEAD
#        scripts/changed-areas.sh --files < list   (one path per line; tests)
# Prints key=true|false lines (for $GITHUB_OUTPUT) saying which parts of
# CI a change between BASE and HEAD needs:
#   go          Go code, modules, embedded data, fixtures, the capture schema,
#               the advisor knowledge base's YAML (kb/),
#               lint/build config
#   nix         the flake, or anything that changes what it builds
#   workflows   CI's own definition (ci.yml, .github/actions/, this script):
#               then everything runs
#   actionlint  any workflow, the actionlint config or a CI script: lint the
#               workflows and run the scripts' own tests
#   docs        Markdown (Mermaid diagrams are rendered to check them), or the
#               docs checks themselves
#   vm          the code the VM checks exercise (the CLI and --full, trust,
#               the system, platform, PCI and firmware collectors and their
#               shared reads, modules) or the VM harness: boot the VMs (#189;
#               the nightly full run boots them for everything else)
#   verify      release verification (verify.yml, scripts/verify-release*.sh):
#               test that it catches tampering, against the live edge release
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
# Non-test files only: a test can't change what a VM boot sees.
match_src() { grep -v '_test\.go$' <<<"$files" | grep -Eq "$1"; }

go=false; nix=false; workflows=false; actionlint=false; docs=false; vm=false; verify=false
match '(\.go$|^go\.(mod|sum|work|work\.sum)$|^vendor/|^(cmd|internal|tools|schema)/|^kb/.*\.ya?ml$|^\.golangci\.ya?ml$|^Makefile$|^scripts/coverage\.sh$)' && go=true
match '^flake\.(nix|lock)$' && nix=true
match '^(\.github/workflows/ci\.yml|\.github/actions/.*|scripts/changed-areas(_test)?\.sh)$' && workflows=true
match '^(\.github/(workflows|actions)/|\.github/actionlint\.ya?ml$|scripts/[^/]+\.sh$)' && actionlint=true
match '(\.md$|^scripts/(check-mermaid|check-adr-index|changed-diagrams(_test)?)\.sh$)' && docs=true
match '^(scripts/vm-[a-z-]+\.sh|\.github/workflows/vms\.yml)$' && vm=true
match_src '^(cmd/hwspec/[^/]+\.go|internal/trust/[^/]+\.go|internal/collect/(collect|system|platform|pci|firmwaretables|sysfs)\.go|go\.(mod|sum)|vendor/.+)$' && vm=true
match '^(scripts/verify-release(_test)?\.sh|\.github/workflows/verify\.yml)$' && verify=true

# The flake builds the Go code; changing CI's own definition re-validates
# everything.
if $go; then nix=true; fi
if $workflows; then go=true; nix=true; actionlint=true; docs=true; vm=true; fi

printf 'go=%s\nnix=%s\nworkflows=%s\nactionlint=%s\ndocs=%s\nvm=%s\nverify=%s\n' "$go" "$nix" "$workflows" "$actionlint" "$docs" "$vm" "$verify"
