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

- `main` is protected: changes land through pull requests that pass CI (the `ci-ok` check).
- Title the PR with [Conventional Commits](https://www.conventionalcommits.org/): `feat: …`, `fix: …`, `docs: …`, `test: …`, `refactor: …`, `perf: …`, `build: …`, `ci: …`, `chore: …`. PRs are squash-merged, so the title becomes the commit message and the release notes entry.
- Keep PRs focused. One feature or fix per PR.

## Tests

Tests describe **behaviour**, not lines: "a laptop whose battery holds 80% of its design capacity reports 80% health", not "call `batteries()`". Collectors are tested against fixture `/sys` and `/proc` trees under `testdata/`, one per representative machine; add a fixture (or extend one) when you add hardware coverage. CI fails below the coverage threshold in `.github/workflows/ci.yml`.

## File format

The capture format is a public interface. Adding fields is fine; renaming, removing or changing the meaning of a field needs a `schema_version` bump and an ADR.

## Dependencies

Dependabot opens weekly update PRs. A Go module update changes the Nix `vendorHash`: the `nix` check then fails and prints `got: sha256-…`; put that value in `flake.nix`.

## ID databases

`make update-ids` refreshes the copies embedded in the binary (do this before a release). The weekly [ids workflow](.github/workflows/ids.yml) publishes the signed bundle that `hwspec ids update` installs. The signing key's public half is in `internal/ids/key.go`; to rotate it, add the new key to `trustedKeys`, release, then change the `HWSPEC_IDS_SIGNING_KEY` secret.

## Releases

Maintainers: run `make update-ids`, merge, then tag `vX.Y.Z` on `main` and push the tag. The release workflow builds, attests and publishes.
