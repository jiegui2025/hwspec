#!/usr/bin/env bash
# Usage: scripts/check-commits.sh BASE HEAD
# Checks every commit in BASE..HEAD against CONTRIBUTING.md › Commits.
# PRs are rebase-merged, so every commit lands on main as written.
#
# Errors (exit 1):
#   - the subject isn't `type(scope)!: summary`, at most 72 characters in all,
#     without a full stop (git revert's own "Revert …" subject included:
#     write `revert: …`)
#   - a fixup!/squash!/amend!/WIP commit
#   - the summary starts with an article or a past or third-person verb
#     ("added", "fixes"): subjects are imperative ("add", "fix")
#   - line 2 isn't blank
#   - a feat, fix, refactor or perf commit has no body besides trailers
#   - `!` without a "BREAKING CHANGE:" footer
# Warnings: body lines over 72 characters (except URLs and indented
# lines), and a closing keyword (fixes #N, in any case, with or without a
# colon) anywhere but a footer line of its own naming one issue: GitHub
# may link the wrong issue, and closes only the first of "Fixes #1, #2".
# The mood check is a word list: it catches articles and past or
# third-person verbs, not every noun phrase.
set -euo pipefail
export LC_ALL=C.UTF-8 # count characters, not bytes
base=${1:?base}; head=${2:?head}
types='feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert'
subject_re="^($types)(\([a-z0-9./_-]+\))?(!)?: [^ ].*$"
# First words that make a subject not imperative.
not_imperative='a|an|the|this|these|any|every|added|adds|adding|fixed|fixes|fixing|updated|updates|updating|removed|removes|removing|changed|changes|changing|made|makes|improved|improves|improving|moved|moves|renamed|renames'
trailer_re='^((Co-Authored-By|Co-authored-by|Signed-off-by|Reviewed-by|BREAKING CHANGE): |(Refs|Fixes|Closes|Resolves):? #[0-9])'
keyword_re='(^|[^[:alnum:]])(fix(es|ed)?|close[sd]?|resolve[sd]?):? ([a-z0-9_.-]+/[a-z0-9_.-]+)?#[0-9]+|(^|[^[:alnum:]])(fix(es|ed)?|close[sd]?|resolve[sd]?):? https?://github\.com/[^ ]+/issues/[0-9]+'
footer_re='^(Fixes|Closes|Resolves|Refs) #[0-9]+\.?$'

bad=0
fail() { echo "::error::${sha:0:7} \"$subject\": $1"; bad=1; }
warn() { echo "::warning::${sha:0:7} \"$subject\": $1"; }

while read -r sha; do
	msg=$(git log -1 --format=%B "$sha" | tr -d '\r')
	subject=$(head -n1 <<<"$msg")
	errors=$bad bad=0
	if [[ $subject =~ ^(fixup!|squash!|amend!|WIP) ]]; then
		fail "fixup, squash and WIP commits are folded in before merging"
	elif [[ ! $subject =~ $subject_re ]]; then
		fail "isn't a Conventional Commit subject: type(scope): summary, with type one of ${types//|/, }"
	else
		breaking=${BASH_REMATCH[3]}
		type=${BASH_REMATCH[1]}
		(( ${#subject} <= 72 )) || fail "the subject is ${#subject} characters; at most 72"
		[[ $subject == *. ]] && fail "no full stop at the end of the subject"
		first=${subject#*: }; first=${first%% *}
		[[ ${first,,} =~ ^($not_imperative)$ ]] && fail "start the summary with an imperative verb (\"add\", not \"$first\")"
		body=$(tail -n +2 <<<"$msg")
		[[ -z $body || -z $(head -n1 <<<"$body") ]] || fail "line 2 must be blank"
		prose=$(grep -Ev "$trailer_re" <<<"$body" | grep -Ev '^\s*$' || true)
		[[ $type =~ ^(feat|fix|refactor|perf)$ && -z $prose ]] && fail "a $type commit says why in its body"
		[[ $breaking == '!' ]] && ! grep -q '^BREAKING CHANGE: ' <<<"$body" && fail "'!' needs a \"BREAKING CHANGE: …\" footer"
		while IFS= read -r line; do
			[[ $line =~ ^[[:space:]] || $line =~ https?:// ]] && continue
			(( ${#line} <= 72 )) || { warn "body line over 72 characters: ${line:0:40}…"; break; }
		done <<<"$body"
		while IFS= read -r line; do
			[[ ${line,,} =~ $keyword_re && ! $line =~ $footer_re ]] && warn "closing keyword outside a one-issue footer line; write \"Fixes #N\" on a line of its own, one per issue: $line"
		done <<<"$msg" # the subject too, and owner/repo#N or issue URLs
	fi
	(( bad )) || echo "ok ${sha:0:7} $subject"
	(( bad |= errors )) || true
done < <(git rev-list --no-merges --reverse "$base..$head")
exit $bad
