#!/usr/bin/env bash
# Usage: scripts/verify-release_test.sh TAG REPO [COMMIT]
#
# Proves scripts/verify-release.sh catches tampering, on a local copy of a
# real release: nothing published changes. Downloads TAG's assets once,
# then checks each case on a fresh copy: the untouched assets pass (first
# and last, so a passing control brackets the failures), and every
# tampered variant fails at the expected check with the expected reason.
# With COMMIT (what verify.yml's integrity job verified), it refuses to run
# if TAG has moved since.
#
# Coverage limits: --signer-workflow and --source-ref cover each other (the
# other-signer case fails on either), and no self-hosted attestation exists
# to test --deny-self-hosted-runners. A static check below makes sure the
# script still passes all of them.
set -euo pipefail
tag=${1:?tag}; repo=${2:?repo}; want_commit=${3:-}
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# gh failed on the release itself: missing, or the lookup couldn't run.
gone() {
	if grep -q 'release not found' <<<"$1"; then
		echo "::error::release '$tag' not found in $repo. For edge: the next push to main whose CI passes republishes it (or re-run the Edge workflow for main's tip)."
	else
		echo "::error::couldn't look up release '$tag' in $repo: $1"
	fi
	exit 1
}
# The commit TAG names, which must not change while the test runs.
moved() {
	echo "::error::$tag is at ${1:-nothing} now, not $2: it moved during the test (a newer publish runs its own verification); re-run if this was by hand"
	exit 1
}
if ! err=$(gh release view "$tag" --repo "$repo" --json tagName 2>&1 >/dev/null); then gone "$err"; fi
commit=$(gh api "repos/$repo/commits/refs/tags/$tag" --jq .sha)
if [ -n "$want_commit" ] && [ "$commit" != "$want_commit" ]; then moved "$commit" "$want_commit"; fi
if ! err=$(gh release download "$tag" --repo "$repo" --dir "$work/orig" --pattern 'hwspec-*.tar.gz' --pattern SHA256SUMS 2>&1); then
	gone "$err"
fi
# Replaced or deleted between the lookup and the download: these files may
# not be the ones that commit built.
now=$(gh api "repos/$repo/commits/refs/tags/$tag" --jq .sha 2>/dev/null || true)
if [ "$now" != "$commit" ]; then moved "$now" "$commit"; fi
amd64=$(cd "$work/orig" && ls hwspec-*-linux-amd64.tar.gz)

fail=0
# The flags must be on the gh attestation verify command itself (comments
# don't count): the command, up to its last continued line.
cmd=$(awk '/^[^#]*gh attestation verify/ { on = 1 } on { sub(/#.*/, ""); print; if ($0 !~ /\\[[:space:]]*$/) on = 0 }' "$here/verify-release.sh")
for flag in --signer-workflow --source-ref --source-digest --deny-self-hosted-runners; do
	grep -q -- "$flag " <<<"$cmd" || { echo "FAIL static: verify-release.sh's gh attestation verify doesn't pass $flag"; fail=1; }
done

# Tampering, run inside a case's copy of the assets.
# shellcheck disable=SC2329 # all are called through case_'s "$@"
append_byte() { printf x >>"$amd64"; }
# shellcheck disable=SC2329
append_byte_and_rehash() { append_byte && sha256sum hwspec-*.tar.gz >SHA256SUMS; }

# Outages: a gh whose attestation calls fail with MESSAGE, in DIR.
real_gh=$(command -v gh)
outage() { # DIR MESSAGE
	mkdir "$1"
	cat >"$1/gh" <<STUB
#!/bin/sh
if [ "\$1 \$2" = "attestation verify" ]; then
	echo "$2" >&2
	exit 1
fi
exec "$real_gh" "\$@"
STUB
	chmod +x "$1/gh"
}
outage "$work/stub-http503" "HTTP 503: Service Unavailable (https://api.github.com/...)"
# What gh prints with no network at all.
outage "$work/stub-offline" "Error: error creating Sigstore verifier: no valid Sigstore verifiers could be initialized"

# case_ NAME EXPECT(pass|checksums|attestation|error) REASON(regex) TAG COMMIT [TAMPER-COMMAND...]
# Runs with $path as PATH (default: the real one).
path=$PATH
case_() {
	local name=$1 want=$2 reason=$3 t=$4 c=$5 d="$work/$1" got
	shift 5
	cp -r "$work/orig" "$d"
	if [ $# -gt 0 ]; then (cd "$d" && "$@"); fi
	if out=$(PATH=$path "$here/verify-release.sh" "$d" "$t" "$repo" "$c" 2>&1); then
		got=pass
	else
		got=$(sed -n 's/^FAIL (\([a-z]*\)).*/\1/p' <<<"$out" | tail -n 1)
	fi
	if [ "$got" = "$want" ] && grep -Eq -- "$reason" <<<"$out"; then
		echo "ok   $name: $want"
	else
		echo "FAIL $name: got '${got:-?}', want '$want' matching /$reason/"
		echo "     ${out//$'\n'/$'\n'     }"
		fail=1
	fi
}

case_ untouched pass '^ok: ' "$tag" "$commit"
case_ tarball-changed checksums "doesn't match SHA256SUMS" "$tag" "$commit" append_byte
# The attacker also rewrites SHA256SUMS: only the attestation catches it.
case_ tarball-and-sums-changed attestation 'HTTP 404: Not Found \(https://api\.github\.com/repos/[^ ]+/attestations/sha256:' \
	"$tag" "$commit" append_byte_and_rehash
case_ tarball-added checksums 'SHA256SUMS lists' "$tag" "$commit" cp "$amd64" hwspec-extra-linux-amd64.tar.gz
case_ tarball-unlisted checksums 'SHA256SUMS lists' "$tag" "$commit" sed -i "/$amd64/d" SHA256SUMS
case_ other-file-added checksums 'unexpected file install.sh' "$tag" "$commit" touch install.sh
case_ other-commit attestation 'expected SourceRepositoryDigest to be 0{40}' "$tag" 0000000000000000000000000000000000000000
other=edge; [ "$tag" = edge ] && other=v0.0.0
# edge.yml's build presented as a release, or the reverse.
case_ other-signer attestation 'verifying with issuer' "$other" "$commit"
case_ bad-tag error "'v1' is not edge" v1 "$commit"
path="$work/stub-http503:$PATH"
case_ api-outage error 'HTTP 503' "$tag" "$commit"
path="$work/stub-offline:$PATH"
case_ offline error 'no valid Sigstore verifiers' "$tag" "$commit"
path=$PATH
case_ untouched-again pass '^ok: ' "$tag" "$commit"

[ "$fail" = 0 ] && echo "verify-release: tampering caught in every case"
exit "$fail"
