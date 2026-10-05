# Contributing to hwspec

Thanks for helping. Bug reports with a `hwspec capture --redact` attached, hardware we don't detect yet, and name corrections are all valuable.

## Development

Go 1.26 or newer. Everything runs without root; collectors that need root are tested against fixture trees.

**Go version policy:** hwspec supports the Go releases the Go team supports (the latest two). `go.mod`'s `go` line is the older of the two, CI tests both, and it moves up when a new Go release ships or a dependency requires it. Update `go.mod` and the `test` matrix in `.github/workflows/ci.yml` together. Release binaries are static, so this only matters when building from source.

```sh
make build    # build/hwspec, static
make test     # go vet + tests
make lint     # golangci-lint (same version and config as CI)
make cover    # tests with coverage, and the coverage gate
```

Read [ARCHITECTURE.md](ARCHITECTURE.md) before larger changes. Design decisions are recorded in [docs/adr/](docs/adr/); a change that alters one needs a new ADR.

## Pull requests

Every change reaches `main` through this flow; the `main` ruleset enforces the PR, the `ci-ok` check and rebase merging.

```mermaid
flowchart TD
  branch["Branch (sibling worktree: wt new hwspec type/topic)"] --> commits["Commits: Conventional Commits, each builds and passes tests"]
  commits --> pr[Open PR]
  pr --> ci{"CI: ci-ok"}
  pr --> review["Independent review (Claude Code agents: correctness, silent failures, security)"]
  review --> fix["Fix every finding (new commits or fixups)"]
  fix --> comment["Post review + resolution table as a PR comment"]
  ci -->|green| ready{All findings closed?}
  comment --> ready
  ready -->|yes| merge["Rebase merge: commits land on main as written"]
  merge --> clean["Branch deleted on GitHub; wt rm / wt prune locally"]
  merge --> close["Linked issues and alerts verified closed"]
```

| Rule | Why |
|---|---|
| **Rebase merges only** | History keeps each logical change; `git bisect` works per commit |
| **Every commit** follows [Conventional Commits](https://www.conventionalcommits.org/) (`type(scope): summary`) | Each lands on `main` and in the release notes; CI's `commits` job checks them |
| **Every commit builds and passes tests** | Bisectability; fold fixes into the commit they fix (`git commit --fixup`, then `git rebase -i --autosquash`) |
| **Independent review before merge**, all findings fixed, review posted on the PR | A second pair of eyes with no context bias; the record stays with the change |
| **One feature or fix per PR** | Reviewable size |

Commit types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`, `style`, `revert`.

### What CI runs

CI runs only what a change needs ([`scripts/changed-areas.sh`](scripts/changed-areas.sh)); `ci-ok` is the single required check, and skipped jobs count as passed.

| The PR changes | Jobs |
|---|---|
| Go code, `go.mod`/`go.sum`, embedded data, lint config, Makefile | lint, tests + coverage gate, govulncheck, static builds, 6-distro smoke tests, Nix |
| `flake.nix` / `flake.lock` only | Nix |
| `.github/workflows/**` or CI scripts | everything, plus actionlint |
| Markdown | Mermaid rendering check (every diagram must render) |
| anything | commit messages, PR title |

Pushes to `main` run everything.

## Tests

Tests describe **behaviour**, not lines: "a laptop whose battery holds 80% of its design capacity reports 80% health", not "call `batteries()`". Collectors are tested against fixture `/sys` and `/proc` trees under `testdata/`, one per representative machine; add a fixture (or extend one) when you add hardware coverage. CI fails below the coverage threshold in `.github/workflows/ci.yml`.

## File format

The capture format is a public interface. Adding fields is fine; renaming, removing or changing the meaning of a field needs a `schema_version` bump and an ADR.

## Dependencies

Dependabot opens weekly update PRs; they follow the same flow (review, then rebase merge).

| Update | Extra step |
|---|---|
| Go module | The Nix `vendorHash` changes: the `nix` check fails and prints `got: sha256-…`. Put it in `flake.nix` **in the same commit** as the update, so every commit builds. |
| Go module requiring a newer Go | Raise `go.mod` and the CI `test` matrix together (see the Go version policy). |
| GitHub Action | Actions are pinned by commit SHA with the version in a comment; Dependabot updates both. |

## ID databases

`make update-ids` refreshes the copies embedded in the binary (do this before a release). The weekly [ids workflow](.github/workflows/ids.yml) publishes the signed bundle that `hwspec ids update` installs:

```mermaid
flowchart LR
  up["Upstream sources (HTTPS only)"] --> build["build job: no secrets, read-only token"]
  build -->|"validate, refuse >5% shrink or future dates"| art[Bundle artifact]
  art --> pub["publish job: ids-signing environment (main only)"]
  pub -->|"re-verify, then sign"| rel["ids-latest + dated ids-YYYY.MM.DD releases"]
```

| Item | Where |
|---|---|
| Signing key (private) | `HWSPEC_IDS_SIGNING_KEY` secret in the `ids-signing` environment, usable only from `main` |
| Public key | `internal/ids/key.go` (`trustedKeys`) |
| Key rotation | add the new public key to `trustedKeys`, release, then replace the environment secret; remove the old key in a later release |
| Expected shrink of a database | rerun the workflow with `HWSPEC_ALLOW_SHRINK=1` after checking the upstream change |

## Releases

Maintainers: run `make update-ids`, merge, then tag `vX.Y.Z` on `main` and push the tag. The release workflow builds, attests and publishes.
