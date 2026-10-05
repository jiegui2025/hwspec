#!/usr/bin/env bash
# Usage: scripts/changed-areas.sh BASE HEAD
# Prints key=true|false lines (for $GITHUB_OUTPUT) saying which parts of
# CI a change between BASE and HEAD needs:
#   go         Go code, modules, embedded data, fixtures, lint/build config
#   nix        the flake, or anything that changes what it builds
#   workflows  CI itself: then everything runs, plus actionlint
#   docs       Markdown (Mermaid diagrams are rendered to check them)
set -euo pipefail
base=${1:?base}; head=${2:?head}

# Three dots: changes since the branch point, not differences from a
# base that has moved on since.
files=$(git diff --no-renames --name-only "$base...$head") # a rename lists both paths
match() { grep -Eq "$1" <<<"$files"; }

go=false; nix=false; workflows=false; docs=false
match '(\.go$|^go\.(mod|sum|work|work\.sum)$|^vendor/|^(cmd|internal|tools)/|^\.golangci\.ya?ml$|^Makefile$|^scripts/coverage\.sh$)' && go=true
match '^flake\.(nix|lock)$' && nix=true
match '^(\.github/(workflows|actions)/|scripts/(changed-areas|check-commits|check-mermaid)\.sh$)' && workflows=true
match '\.md$' && docs=true

# The flake builds the Go code; changing CI re-validates everything.
if $go; then nix=true; fi
if $workflows; then go=true; nix=true; docs=true; fi

printf 'go=%s\nnix=%s\nworkflows=%s\ndocs=%s\n' "$go" "$nix" "$workflows" "$docs"
