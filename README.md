# hwspec

[![CI](https://github.com/jiegui2025/hwspec/actions/workflows/ci.yml/badge.svg)](https://github.com/jiegui2025/hwspec/actions/workflows/ci.yml)
[![ID databases](https://github.com/jiegui2025/hwspec/actions/workflows/ids.yml/badge.svg)](https://github.com/jiegui2025/hwspec/releases/tag/ids-latest)
[![License: GPL-3.0-or-later](https://img.shields.io/badge/license-GPL--3.0--or--later-blue)](LICENSE)

**Capture a Linux machine's complete hardware specification to a JSON, YAML or text file. One static binary, any distro, works offline.**

```console
$ hwspec capture -f text
hwspec v0.1.0  ·  captured 2026-10-05 12:00 UTC  ·  limited (not root)

System
  Machine    HP EliteDesk 800 G5 Desktop Mini
  Chassis    Mini Tower
  Board      HP 8595
  Firmware   HP R21 Ver. 02.27.00 07/28/2026
  OS         CachyOS, kernel 7.2.8-1-cachyos (x86_64, uefi, Secure Boot off)

CPU
  Model      Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz
  Codename   Coffee Lake (Skylake cores)
  Cores      6 cores / 6 threads
  Clock      800–3700 MHz (intel_pstate built in)
  Cache      L1d 32 KiB×6, L1i 32 KiB×6, L2 256 KiB×6, L3 9 MiB×1
  Microcode  0xfa
  Health     health OK, 0 throttle events

Memory
  Total      31.1 GiB usable
  SPD 7-0050 16 GiB DDR4 SODIMM Avant Technology J642GU44J2320NL

Storage
  nvme0n1    SAMSUNG MZVLB256HAHQ-000L7 238.5 GiB nvme, fw 1L2QEXD7, nvme 1.0

Graphics
  GPU        Intel Corporation CoffeeLake-S GT2 [UHD Graphics 630], i915
  Display    DELL S2721QS, 3840×2160 @ 60 Hz, 27.0", card1-DP-3

Network
  eno1       Intel Corporation Ethernet Connection (7) I219-LM, ethernet, up, 1000 Mb/s, fw 0.5-4, e1000e
  …

Not captured
  - dmi product_serial, product_uuid, chassis_serial, board_serial: needs root (run with --full)
  - drive health (SMART): needs root (run with --full)
  - memory modules (SMBIOS): needs root (run with --full)
```

## Why hwspec

| | hwspec | lshw | inxi | dmidecode | hwinfo |
|---|---|---|---|---|---|
| Install | one static binary | distro package (C++) | distro package (Perl, calls helper tools) | distro package (C) | distro package (C) |
| Same result on every distro (incl. NixOS, Alpine) | ✅ CI-tested on 6 | depends on version | depends on installed tools | depends on version | depends on version |
| Structured output | JSON/YAML, versioned schema | JSON/XML | JSON/XML (`--output`) | text | text |
| Names hardware offline, signed ID updates | ✅ | distro ID files | distro ID files | — | own database |
| Keeps raw IDs to re-name old captures | ✅ | IDs in output | partial | — | IDs in output |
| Memory modules, NVMe health, EDID, Bluetooth, codecs, CPU codenames | ✅ all | partial | most, via helper tools | memory and firmware tables | partial |
| Reports what it couldn't read | ✅ a `warnings` list | root warning | per-field markers | — | — |
| Share safely | ✅ `--redact` | ✅ `-sanitize` | ✅ `-z` filter | — | — |

## How it works

```mermaid
flowchart LR
  k["kernel: /sys, /proc,<br/>SMBIOS, EDID, ioctls"] --> c[collect raw facts]
  c --> n["name from IDs<br/>(offline databases)"]
  n --> s[sanitise untrusted strings]
  s --> o["JSON · YAML · text"]
  saved[(saved capture)] -->|hwspec show| n
```

It reads the kernel's interfaces directly instead of parsing `lshw`, `dmidecode` or `lspci` output, which may not be installed. Details: [ARCHITECTURE.md](ARCHITECTURE.md).

## What it captures

Every device carries the same four blocks wherever the hardware exposes them ([ADR 0008](docs/adr/0008-device-detail-blocks.md)):

```mermaid
flowchart LR
  dev[each device] --> id["identity<br/>vendor, model, part no., serial,<br/>revision, manufacture date"]
  dev --> fw["firmware<br/>version, date, source"]
  dev --> drv["driver<br/>module, version, in-tree,<br/>proprietary, signed"]
  dev --> h["health<br/>ok · warning · failing, reasons,<br/>life used / remaining, metrics"]
```

| Device | Identity | Firmware | Driver | Health / longevity |
|---|---|---|---|---|
| System, board | model, SKU, serial¹, UUID¹ | BIOS/UEFI version, date | — | — |
| CPU | model, signature, codename, microarchitecture | microcode | frequency scaling | thermal throttling since boot |
| RAM modules | maker, part no., serial, manufacture date (SPD, no root needed); slot, type, speed, rank (SMBIOS¹) | — | — | ECC errors (EDAC) |
| Disks | model, serial | ✅ | controller (nvme, ahci, usb-storage…) | SMART¹: status, % life used/left, hours, data written, errors |
| GPUs, monitors | model, revision; monitor serial and manufacture date (EDID) | AMD/NVIDIA video BIOS | ✅ | — |
| Network, Wi-Fi, Bluetooth | model, MAC and its vendor | via ethtool | ✅ | error and drop rates |
| Audio | card, codec chips | — | ✅ | — |
| Batteries | model, serial, manufacture date | — | — | wear %, cycles, est. cycles until 80% |
| PCI and USB devices | IDs, names, serial, revision | USB device release | ✅ per interface | — |

Plus: OS, kernel, boot mode, Secure Boot, VM/container; CPU cores, caches, clocks, flags; partitions; PCIe links; IOMMU groups; sensors (temperatures, fans, voltages, power). Text output ends with a **Needs attention** list of every warning and failure, with reasons.

¹ Needs root: `--full`. Life estimates only come from the hardware's own wear indicators and say how they were computed; rated values (TBW, rated cycles) come later from model data ([#25](https://github.com/jiegui2025/hwspec/issues/25)).

## Install

| Method | Command |
|---|---|
| Release binary (amd64, arm64), from v0.1.0 | download a tarball from [releases](https://github.com/jiegui2025/hwspec/releases), `tar xzf hwspec-*.tar.gz`, then `sudo install -m755 hwspec /usr/local/bin/` |
| From source (Go 1.26+) | `make build && sudo make install` |
| Nix flakes / NixOS | `nix run github:jiegui2025/hwspec -- capture -f text` |

Install to a root-owned location (as above) if you want `--full`; see [Root access](#root-access).

## Usage

| Command | Does |
|---|---|
| `hwspec capture -o spec.json` | capture to JSON (format follows the extension: `.json`, `.yaml`, `.txt`) |
| `hwspec capture -f text` | readable summary on stdout |
| `hwspec capture --full -o spec.json` | include root-only data (asks via polkit) |
| `hwspec capture --redact -o share.json` | strip serials, UUIDs, MACs, hostname, personal paths (captures of one machine stay linkable: see [SECURITY.md](SECURITY.md#what-redaction-doesnt-do)) |
| `hwspec show spec.json` | summarise a capture, with names refreshed from today's databases |
| `hwspec show old.json -o new.json [--redact]` | re-export a capture with refreshed names (optionally redacted) |
| `hwspec ids` | which ID database sources are in use |
| `hwspec ids lookup pci 8086:3e92` | resolve one ID |
| `hwspec ids update [--check]` | install the latest signed databases |

### Root access

```mermaid
sequenceDiagram
  participant U as hwspec (you)
  participant P as pkexec
  participant R as hwspec (root)
  U->>U: binary root-owned? (else refuse)
  U->>P: capture -f json
  P->>R: run after you approve
  R-->>U: JSON
  U->>U: your overrides, file written as you (0600 unless redacted)
```

| Without root | With `--full` |
|---|---|
| Everything except the items below; skipped items listed in `warnings` | + memory modules (SMBIOS), serial numbers and UUID, drive health |

`--full` only elevates a binary **only root can modify**, so malware running as you can't swap it before you approve the prompt. Otherwise run `sudo hwspec capture …`; the file is handed back to you.

## Hardware ID databases

Captures store raw IDs; names come from eight databases. HD Audio codecs need none: the kernel names them.

| Database | Translates | Upstream | Licence |
|---|---|---|---|
| `pci` | PCI vendors, devices, subsystems, classes | [pci-ids.ucw.cz](https://pci-ids.ucw.cz) | GPL-2.0-or-later or BSD-3-Clause |
| `usb` | USB vendors, products, classes | [linux-usb.org](http://www.linux-usb.org/usb-ids.html), via the [hwdata](https://github.com/vcrhonek/hwdata) mirror (HTTPS) | GPL-2.0-or-later or BSD-3-Clause |
| `pnp` | Monitor makers (EDID) | [hwdata](https://github.com/vcrhonek/hwdata) | GPL-2.0-or-later |
| `oui` | MAC address prefixes | [IEEE registry](https://standards-oui.ieee.org/) | public listing |
| `jedec` | Memory makers (JEP106), including codes like `80CE` or HP's `Unknown - [0xF785]` | [i2c-tools](https://git.kernel.org/pub/scm/utils/i2c-tools/i2c-tools.git) `decode-dimms` | GPL-2.0-or-later |
| `amdgpu` | AMD GPU retail names by device and revision | [libdrm](https://gitlab.freedesktop.org/mesa/drm) | MIT |
| `bluetooth` | Bluetooth chip makers (SIG company IDs) | [Bluetooth SIG assigned numbers](https://bitbucket.org/bluetooth-SIG/public) | published by the Bluetooth SIG |
| `cpu` | CPU codename and core microarchitecture | Linux kernel [`intel-family.h`](https://github.com/torvalds/linux/blob/master/arch/x86/include/asm/intel-family.h), [`amd.c`](https://github.com/torvalds/linux/blob/master/arch/x86/kernel/cpu/amd.c), plus [a curated list](tools/genids/cpu-curated.ids) that cites a source for every entry | GPL-2.0 (kernel); curated list GPL-3.0-or-later |

### Where names come from

```mermaid
flowchart LR
  emb[embedded in the binary] --> pick{newest readable}
  dis["your distro's copy"] --> pick
  syn["synced (hwspec ids update)"] --> pick
  pick --> ov[+ your overrides] --> names[names]
```

| Source | Dated by | Notes |
|---|---|---|
| Embedded | its manifest | always available, offline |
| Distro (`/usr/share/hwdata`, `/usr/share/libdrm`, …) | version header, else file date | updated by your package manager |
| Synced (`~/.local/share/hwspec/ids/`) | its signed manifest | `hwspec ids update` |
| Overrides (`~/.config/hwspec/overrides.ids`) | applied last | `hwspec ids template` prints a starting file |

### Keeping the databases current

A [weekly workflow](.github/workflows/ids.yml) rebuilds all eight databases from upstream and publishes a signed bundle as [`ids-latest`](https://github.com/jiegui2025/hwspec/releases/tag/ids-latest).

| `hwspec ids update` step | Guarantee |
|---|---|
| verify the manifest's ed25519 signature | only bundles signed by the key built into hwspec |
| compare with installed and built-in data | no rollback to older data |
| download only changed databases; check size, SHA-256, contents | nothing unverified is installed |
| replace each file atomically, manifest last | an interrupted update leaves only verified files; the next update completes it |

Only the HTTP request leaves the machine; captures never touch the network. `HWSPEC_IDS_URL` (or `--url`) selects a mirror.

### Correcting a name

Add a line to `~/.config/hwspec/overrides.ids`, then please submit the correction upstream too (e.g. [pci-ids.ucw.cz](https://pci-ids.ucw.cz)):

```
pci 8086:3e92 = UHD Graphics 630
usb 046d:c52b = Logitech Unifying Receiver
jedec F785 = Avant Technology
oui 04:0E:3C = HP Inc.
```

## File format

| Aspect | Rule |
|---|---|
| Format | JSON (canonical) or YAML with the same field names |
| Versioning | `schema_version` (currently 1) changes only when a field is renamed, removed or changes meaning; new fields can appear without a bump |
| Units | bytes, °C, or named in the field (`_mhz`, `_mts`, `_mbps`) |
| Missing data | omitted, empty or "unknown", with the reason in `warnings`; never guessed |
| Reference | the Go structs in [internal/report/report.go](internal/report/report.go); a JSON Schema is planned ([#37](https://github.com/jiegui2025/hwspec/issues/37)) |

## Roadmap

```mermaid
flowchart LR
  v1["v0.1.0 — MVP CLI<br/>release, deployment,<br/>verified on real distros"] --> v2["v0.2.0 — Advisor<br/>drivers, firmware, upgrades,<br/>maintenance reminders"] --> v3["v0.3.0 — Desktop app<br/>review, compare, export"]
```

Items are listed in board rank: priority first, then dependencies (a P1 that waits on others comes after them). The [project board](https://github.com/users/jiegui2025/projects/6) holds the same ranking and each item's status; every issue carries its evidence, full solution and acceptance criteria ([how backlog items are written](CONTRIBUTING.md#backlog)).

### v0.1.0 — MVP CLI: a signed, verified first release

| # | Item | Priority | Size |
|---|---|---|---|
| [#53](https://github.com/jiegui2025/hwspec/issues/53) | chore(board): add PRs to the board and move linked issues automatically | P1 | XS |
| [#38](https://github.com/jiegui2025/hwspec/issues/38) | chore: back up the ID-signing key offline | P1 | XS |
| [#22](https://github.com/jiegui2025/hwspec/issues/22) | ci: gated deployment to edge and release, verified on real distros afterwards | P1 | L |
| [#37](https://github.com/jiegui2025/hwspec/issues/37) | feat(cli): publish a JSON Schema for the capture format, with a compatibility check | P2 | M |
| [#27](https://github.com/jiegui2025/hwspec/issues/27) | ci: cover Linux Mint and MX Linux (sysvinit) explicitly | P2 | M |
| [#29](https://github.com/jiegui2025/hwspec/issues/29) | ci: enforce the architecture rules automatically | P2 | S |
| [#3](https://github.com/jiegui2025/hwspec/issues/3) | chore: release v0.1.0 | P1 | S |

### v0.2.0 — Advisor: turn a capture into advice

| # | Item | Priority | Size |
|---|---|---|---|
| [#5](https://github.com/jiegui2025/hwspec/issues/5) | research(advisor): advisor design (ADR 0009) | P1 | M |
| [#26](https://github.com/jiegui2025/hwspec/issues/26) | research(capture): feature parity with Defenestra Chassis | P2 | S |
| [#7](https://github.com/jiegui2025/hwspec/issues/7) | feat(advisor): needs-attention list for devices without drivers or firmware | P1 | L |
| [#25](https://github.com/jiegui2025/hwspec/issues/25) | data(advisor): reference database of replacement and upgrade part numbers | P1 | L |
| [#43](https://github.com/jiegui2025/hwspec/issues/43) | feat(capture): remaining firmware sources (amdgpu blocks, Intel GuC/HuC, Bluetooth HCI revision) and missing SPD EEPROMs | P2 | L |
| [#10](https://github.com/jiegui2025/hwspec/issues/10) | feat(advisor): firmware versions and update paths | P2 | L |
| [#8](https://github.com/jiegui2025/hwspec/issues/8) | feat(advisor): driver and configuration alternatives for better performance | P2 | L |
| [#9](https://github.com/jiegui2025/hwspec/issues/9) | feat(advisor): upgrade and replacement options for key components | P2 | M |
| [#11](https://github.com/jiegui2025/hwspec/issues/11) | feat(advisor): maintenance reminders | P2 | L |
| [#18](https://github.com/jiegui2025/hwspec/issues/18) | feat(capture): SATA and USB (SAT) drive health without smartctl | P2 | L |
| [#12](https://github.com/jiegui2025/hwspec/issues/12) | feat(cli): hwspec diff to compare captures | P3 | M |
| [#35](https://github.com/jiegui2025/hwspec/issues/35) | data(advisor): correct firmware-reported chassis types from model data | P3 | S |

### v0.3.0 — Desktop app: a native Linux app ([ADR 0007](docs/adr/0007-desktop-ui.md))

| # | Item | Priority | Size |
|---|---|---|---|
| [#13](https://github.com/jiegui2025/hwspec/issues/13) | research(ui): desktop app spike: memory, large lists, Flatpak host access and --full (ADR 0007) | P2 | L |
| [#14](https://github.com/jiegui2025/hwspec/issues/14) | feat(ui): desktop app to review, compare and export captures (tracking) | P2 | XL |

### Unscheduled: when capacity allows; deferred items are marked

| # | Item | Priority | Size |
|---|---|---|---|
| [#17](https://github.com/jiegui2025/hwspec/issues/17) | chore(ids): refresh the embedded ID databases automatically, and refuse stale ones at release | P2 | S |
| [#20](https://github.com/jiegui2025/hwspec/issues/20) | feat(packaging): .deb, .rpm and AUR hwspec-bin from the release workflow | P2 | M |
| [#15](https://github.com/jiegui2025/hwspec/issues/15) | ci: update the Nix vendorHash automatically on Dependabot Go module PRs | P3 | S |
| [#16](https://github.com/jiegui2025/hwspec/issues/16) | chore: keep the committed genids binary in history, and block new build artifacts in CI | P3 | XS |
| [#30](https://github.com/jiegui2025/hwspec/issues/30) | ci: automate the independent Claude Code review on every PR *(deferred)* | P3 | M |
| [#52](https://github.com/jiegui2025/hwspec/issues/52) | ci: keep CI running when GitHub-hosted runners are unavailable *(deferred)* | P3 | L |
| [#28](https://github.com/jiegui2025/hwspec/issues/28) | feat: Windows and macOS support *(deferred)* | P3 | XL |
| [#57](https://github.com/jiegui2025/hwspec/issues/57) | ci: move the pinned runner image to ubuntu-26.04 | P3 | XS |

## Building

| Command | Does |
|---|---|
| `make build` | `build/hwspec`, static (CGO off) |
| `make test` / `make lint` / `make cover` | tests, golangci-lint, coverage gate |
| `make release` | amd64 + arm64 tarballs with `SHA256SUMS` |
| `make update-ids` | refresh the embedded ID databases from upstream |

| Path | What |
|---|---|
| `cmd/hwspec` | CLI, including the `pkexec` re-run for `--full` |
| `internal/collect` | one collector per area, reading sysfs/procfs; CPU and block devices via [ghw](https://github.com/jaypipes/ghw) |
| `internal/ids` | ID databases, overrides, sync, JEDEC/OUI/CPU decoding |
| `internal/resolve` | names from raw IDs, for new and saved captures |
| `internal/report`, `internal/output` | the file format, redaction, sanitising; JSON/YAML/text |
| `internal/smbios`, `internal/edid`, `internal/spd`, `internal/trust` | binary parsers (SMBIOS, EDID, RAM SPD); root-ownership checks |
| `tools/genids` | ID bundle builder |
| `tools/snapshot` | records a machine as a scrubbed test fixture |

## Contributing

| Read | For |
|---|---|
| [CONTRIBUTING.md](CONTRIBUTING.md) | development, tests, the PR and review flow |
| [ARCHITECTURE.md](ARCHITECTURE.md), [docs/adr/](docs/adr/) | how the code is organised and why |
| [SECURITY.md](SECURITY.md) | reporting a vulnerability |
| [Roadmap](#roadmap), [project board](https://github.com/users/jiegui2025/projects/6) | what's next, defects and maintenance |

## Licence

GPL-3.0-or-later; see [LICENSE](LICENSE). The embedded ID databases keep their own licences, listed above.
