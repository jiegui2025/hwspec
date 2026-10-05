# Architecture

hwspec turns what the Linux kernel knows about a machine into a stable, portable file. This document describes how the code is organised and the rules that keep it that way. The reasons behind each major choice are in the [decision records](docs/adr/).

## Data flow

```
 kernel interfaces              capture engine                       outputs
 ─────────────────              ──────────────                       ───────
 /sys, /proc,          ┌──────────┐   ┌───────────┐   ┌─────────┐   JSON / YAML / text
 SMBIOS table,  ─────▶ │ collect  │──▶│  report   │──▶│ resolve │──▶ (output)
 ioctls, mgmt socket   │ raw facts│   │ (format)  │   │ names   │
                       └──────────┘   └───────────┘   └────┬────┘
                                            ▲              │ lookups
                       saved capture ───────┘         ┌────▼────┐   embedded ┐
                       (hwspec show)                  │   ids   │◀─ distro   ├ newest wins,
                                                      └─────────┘   synced   ┘ then overrides
```

1. **collect** reads raw facts: IDs, sizes, versions, states. It never names things from databases.
2. **report** is the file format: plain structs with JSON tags, no behaviour beyond redaction.
3. **resolve** fills human-readable names from the raw IDs. It runs after every capture *and* when a saved capture is shown, so old files benefit from newer databases.
4. **ids** owns the ID databases: loading, choosing the newest source, overrides, and signed sync.
5. **output** writes JSON, YAML or text and reads captures back.

## Packages

| Package | Responsibility | May depend on |
|---|---|---|
| `cmd/hwspec` | CLI parsing, `pkexec` re-run, wiring | everything below |
| `internal/collect` | Reading kernel interfaces into a `report.Report` | `report`, `resolve`, `smbios`, `edid`, `ghw` |
| `internal/resolve` | IDs → names on a report | `ids`, `report` |
| `internal/ids` | ID databases, overrides, sync, decoders (JEDEC, OUI, CPU) | standard library only |
| `internal/report` | The file format and redaction | standard library only |
| `internal/output` | Serialisation | `report` |
| `internal/smbios`, `internal/edid` | Pure parsers for binary tables | standard library only |
| `tools/genids` | Build-time: turns upstream sources into ID database files | `ids` |

## Rules

- **The file format is a public interface.** Additive changes only, unless `schema_version` is bumped with an ADR. Fields that couldn't be read are omitted or empty, never guessed. Raw IDs are always stored next to names.
- **Collectors degrade, they don't fail.** A missing file, a permission error or an absent subsystem leaves fields empty and, where it helps the user, adds a `warnings` entry. Only a broken invariant is an error.
- **No external tools in the capture path** except `smartctl` for SATA health, which is optional. Everything else comes from kernel interfaces, so one static binary works on every distro.
- **Captures never touch the network.** Only `hwspec ids update` does, and it only installs signed, verified data.
- **Root is opt-in and minimal.** `--full` re-runs the binary under `pkexec`; the unprivileged parent writes the file and applies the user's own overrides.
- **Parsers are pure.** Binary formats (SMBIOS, EDID, NVMe SMART log, Bluetooth management replies, ID files) are parsed by functions over bytes, tested without hardware.
- **Testable seams.** All filesystem reads in `collect` go through one root path, so fixture trees under `testdata/` stand in for real machines; syscalls and subprocesses sit behind small swappable functions.

## Distribution

- `CGO_ENABLED=0` static binaries for amd64 and arm64, with SHA-256 sums and build provenance attestations.
- A Nix flake for NixOS.
- ID databases: embedded in the binary, and published weekly as a signed bundle (`ids-latest` release).

## Planned

- **Advisor** (upgrade options, firmware, drivers, maintenance reminders): a rules engine over captures with a curated, synced knowledge base. Design in progress.
- **Desktop app**: see [ADR 0007](docs/adr/0007-desktop-ui.md).
