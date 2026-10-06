#!/usr/bin/env bash
# Usage: scripts/check-commits_test.sh
# Runs scripts/check-commits.sh on throwaway commits: each rule must fail a
# bad message, with its own message, and pass a good one, or it checks
# nothing.
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/check-commits.sh
repo=$(mktemp -d); trap 'rm -rf "$repo"' EXIT
# The machine's own git config (hooks, templates, signing) stays out.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
git -C "$repo" init -q
git -C "$repo" commit -q --allow-empty -m "chore: start"
base=$(git -C "$repo" rev-parse HEAD)

fail=0
# expect pass|fail|warn WHY MESSAGE...: commit each MESSAGE on top of base,
# check the range, and for fail or warn require WHY in the output.
expect() {
	local want=$1 why=$2 out code=0 msg
	shift 2
	git -C "$repo" reset -q --hard "$base"
	for msg in "$@"; do
		git -C "$repo" commit -q --allow-empty --cleanup=verbatim -m "$msg"
	done
	# LC_ALL=C: the script must set its own locale to count characters.
	out=$(cd "$repo" && LC_ALL=C "$script" "$base" HEAD 2>&1) || code=$?
	case $want in
	pass) [[ $code == 0 && $out != *::warning::* && $out != *::error::* ]] ;;
	warn) [[ $code == 0 && $out == *::warning::*"$why"* ]] ;;
	fail) [[ $code == 1 && $out == *::error::*"$why"* ]] ;;
	esac || { echo "FAIL: want $want ($why), got exit $code for: ${1%%$'\n'*}"; echo "    ${out//$'\n'/$'\n'    }"; fail=1; }
}

body=$'\n\nThe old code did X, which broke Y.\n\nRefs #1'
expect pass "" "feat(cli): add the advise command$body"
expect pass "" "docs: fix a typo"
expect pass "" "ci(release)!: sign with the new key$body"$'\nBREAKING CHANGE: older clients reject the bundle'
expect pass "" "fix(ids): name AMD family 17h model 47h$body"$'\n\nFixes #75\nFixes #76'
expect pass "" "fix(cli): stop the crash"$'\n\nFixes the crash on boot when /sys is read-only.'
expect pass "" "docs: say what an ellipsis … means in the long summary that just fits ok"
expect pass "" "docs: tidy"$'\n\nSee https://example.invalid/a/very/long/url/that/cannot/be/wrapped/anywhere/sensible/at/all'
expect pass "" "docs: tidy"$'\r\n\r\nWindows line endings are fine.\r\n'
expect fail "isn't a Conventional Commit" "Add the advise command$body"
expect fail "isn't a Conventional Commit" $'Revert "feat(cli): add the advise command"\n\nThis reverts commit 0123456.'
expect fail 'not "Added"' "feat: Added the advise command$body"
expect fail 'not "the"' "fix: the parser no longer crashes$body"
expect fail "at most 72" "feat(cli): add a command whose summary is far too long to read in a one-line log$body"
expect fail "no full stop" "docs: fix a typo."
expect fail "at most 72" "docs: $(printf 'x%.0s' {1..67})"
expect pass "" "docs: $(printf 'x%.0s' {1..66})"
expect fail "a perf commit says why" "perf(ids): load the tables once"
expect fail "a refactor commit says why" "refactor(output): split the writer"
expect fail 'not "an"' "docs: an example of the format"
expect fail 'not "every"' "test: every recorded capture validates"
expect pass "" "docs: tidy"$'\n\n    an indented code or log line may run past seventy-two characters, unwrapped'
expect pass "" "fix(ids): name a CPU$body"$'\n\nCloses #31.'
expect warn "closing keyword" "fix(ids): name a CPU, closes #31$body"
expect warn "closing keyword" "fix(ids): name a CPU$body"$'\n\nFixes jiegui2025/hwspec#31'
expect warn "closing keyword" "fix(ids): name a CPU$body"$'\n\nResolves https://github.com/jiegui2025/hwspec/issues/31'
expect warn "closing keyword" "fix(ids): name a CPU$body"$'\n\nFixes: #31'
expect fail "a fix commit says why" $'fix(ids): name a CPU\n\nFixes: #31' 
expect fail "folded in before merging" "fixup! feat(cli): add the advise command"
expect fail "folded in before merging" "WIP: advise"
expect fail "line 2 must be blank" $'docs: tidy\nno blank line'
expect fail "a feat commit says why" "feat(cli): add the advise command"
expect fail "a fix commit says why" $'fix(cli): stop crashing\n\nRefs #3\nCo-Authored-By: A <a@example.invalid>'
expect fail "BREAKING CHANGE" "feat(cli)!: drop the show command$body"
# One bad commit fails the range, even when a good one follows.
expect fail "isn't a Conventional Commit" "Add things" "feat(cli): add the advise command$body"
expect warn "body line over 72" "docs: tidy"$'\n\nThis body line is much longer than seventy-two characters and should be wrapped.'
expect warn "closing keyword" "fix(ids): name a CPU"$'\n\nThe name was wrong; this fixes #12 by using the kernel table.'
expect warn "one per issue" "fix(ids): name two CPUs$body"$'\n\nFixes #12, #13'
expect warn "closing keyword" "fix(ids): name a CPU$body"$'\n\nCLOSES #12'
expect warn "closing keyword" "fix(ids): name a CPU$body"$'\n\nCloses: #12'

[ "$fail" = 0 ] && echo "check-commits: all cases pass"
exit "$fail"
