#!/usr/bin/env bash
# shellcheck disable=SC2016 # the Markdown below holds literal backticks
# Usage: scripts/changed-diagrams_test.sh
# Checks that changed-diagrams.sh lists a Markdown file exactly when its
# change touches a Mermaid block: CI renders only those, so a miss would
# let a broken diagram through.
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/changed-diagrams.sh
# The scratch repository must be the only one this test touches. git
# exports GIT_DIR and friends to hooks and `rebase --exec`; left set, the
# git commands below would reconfigure and commit to the caller's
# repository instead (this happened once: core.bare and user.* in the
# shared config). So drop them, write no config, and check where git
# points before changing anything.
# shellcheck disable=SC2046 # one variable name per word
unset $(git rev-parse --local-env-vars)
export GIT_CONFIG_NOSYSTEM=1 GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@invalid \
  GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@invalid
repo=$(mktemp -d); trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q
if [ "$(git rev-parse --absolute-git-dir)" != "$repo/.git" ]; then
  echo "FAIL: git points at $(git rev-parse --absolute-git-dir), not the scratch repository"
  exit 1
fi
doc() { # NAME: a page with prose, a diagram, more prose
  printf '# %s\n\nIntro.\n\n```mermaid\nflowchart LR\n  a --> b\n```\n\nOutro.\n' "$1" > "$1.md"
}
for f in prose inside fence addblock delete untouched nodiagram; do doc "$f"; done
printf 'no diagram here\n' > nodiagram.md
git add -A && git commit -qm base && base=$(git rev-parse HEAD)

sed -i 's/Outro\./Outro, edited./' prose.md                 # prose only
sed -i 's/a --> b/a --> c/' inside.md                       # inside the block
sed -i 's/^```mermaid$/```mermaid  /' fence.md              # the opening fence
printf '\n```mermaid\nflowchart TD\n  x --> y\n```\n' >> addblock.md # a new block
sed -i '/a --> b/d' delete.md                                # a line deleted inside
printf 'edited\n' >> nodiagram.md                            # a file without diagrams
git add -A && git commit -qm change && head=$(git rev-parse HEAD)

got=$("$script" "$base" "$head" | tr '\0' '\n' | sort | paste -sd' ')
want="addblock.md delete.md fence.md inside.md"
if [ "$got" != "$want" ]; then
  echo "FAIL: got '$got', want '$want'"
  exit 1
fi
echo "changed-diagrams: all cases pass"
