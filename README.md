# hwspec

[![CI](https://github.com/jiegui2025/hwspec/actions/workflows/ci.yml/badge.svg)](https://github.com/jiegui2025/hwspec/actions/workflows/ci.yml)
[![ID databases](https://github.com/jiegui2025/hwspec/actions/workflows/ids.yml/badge.svg)](https://github.com/jiegui2025/hwspec/releases/tag/ids-latest)
[![License: GPL-3.0-or-later](https://img.shields.io/badge/license-GPL--3.0--or--later-blue)](LICENSE)

**Capture a Linux machine's complete hardware specification to a JSON, YAML or text file. One static binary, any distro, works offline.**

```console
$ hwspec capture --full -f text
System
  Machine    HP EliteDesk 800 G5 Desktop Mini
  Board      HP 8595 KBC Version 08.09.22
  BIOS       HP R21 Ver. 02.27.00 07/28/2026
  OS         CachyOS, kernel 7.2.8-1-cachyos (x86_64, uefi, Secure Boot off)

CPU
  Model      Intel(R) Core(TM) i5-9500T CPU @ 2.20GHz
  Cores      6 cores / 6 threads
  Cache      L1d 32 KiB×6, L1i 32 KiB×6, L2 256 KiB×6, L3 9 MiB×1

Memory
  Total      32 GiB installed, 31.1 GiB usable
  DIMM1      16 GiB DDR4 SODIMM 2667 MT/s (rated 3200) Avant Technology J642GU44J2320NL
  DIMM3      16 GiB DDR4 SODIMM 2667 MT/s (rated 3200) Avant Technology J642GU44J2320NL

Storage
  nvme0n1    SAMSUNG MZVLB256HAHQ-000L7 238.5 GiB nvme, health OK, 4% worn, 2541 h on

Graphics
  GPU        Intel Corporation CoffeeLake-S GT2 [UHD Graphics 630] (i915)
  Display    Dell Inc. DELL S2721QS, 3840×2160 @ 60 Hz, 27.0" (card1-DP-3)
...
```

## Why

- **Runs everywhere.** A single statically linked binary with no runtime dependencies. Tested on CachyOS/Arch, Fedora, Ubuntu, Linux Mint, Debian (and so MX Linux), NixOS and Alpine.
- **Doesn't depend on other tools.** It reads the kernel's `/sys` and `/proc` interfaces and the SMBIOS table directly, instead of parsing `lshw`, `dmidecode` or `lspci` output that may not be installed.
- **Names hardware offline.** PCI, USB, monitor, MAC-prefix, memory-maker and AMD GPU IDs are translated from databases embedded in the binary, layered with your distro's copies and your own corrections.
- **Keeps the raw IDs.** Captures store vendor/device IDs next to the names, so `hwspec show` can re-name an old capture with newer databases.
- **Honest about gaps.** Anything it couldn't read is listed under `warnings`, never guessed.

## What it captures

| Area | Details |
|---|---|
| System | Vendor, model, family, SKU, chassis type, serial and UUID¹; OS, kernel, init, boot mode, Secure Boot, VM/container detection |
| Board & BIOS | Board vendor, model, version, serial¹; firmware vendor, version, date |
| CPU | Model, codename and core microarchitecture (e.g. Coffee Lake / Skylake, Raphael / Zen 4), signature, sockets, cores, threads, P/E core split, clock range, scaling driver, caches, microcode, flags |
| Memory | Usable and installed size, slots, max capacity, ECC; per module¹: slot, size, type (DDR4/DDR5…), form factor, rated and configured speed, voltage, rank, manufacturer, part number |
| Storage | Model, serial, firmware, size, type, transport (NVMe/SATA/USB/…), partitions, filesystems, mount points; health¹: NVMe SMART log (wear, hours, data written, errors) or `smartctl` for SATA |
| Graphics | GPUs with driver, VRAM (amdgpu), PCIe link, outputs; monitors from EDID: maker, model, serial, size, native mode |
| Network | Physical adapters: type, driver, bus, MAC and its registered vendor, link state, speed |
| Bluetooth | Controllers: chip maker, Bluetooth version, address and its vendor, power state, the USB/PCI adapter (no root or bluetoothd needed) |
| Audio | Sound cards with their HD Audio codec chips (e.g. Realtek ALC897) |
| Other | Batteries (wear, cycles), sensors (temperatures, fans, voltages, power), every PCI device (class, driver, IOMMU group, PCIe link) and USB device |

¹ Needs root: run with `--full`.

## Install

Download a release binary (amd64 or arm64) from the [releases page](https://github.com/jiegui2025/hwspec/releases), or build from source with Go 1.22+:

```sh
go install github.com/jiegui2025/hwspec/cmd/hwspec@latest
```

On NixOS, or anywhere with Nix flakes:

```sh
nix run github:jiegui2025/hwspec -- capture -f text
```

## Usage

```sh
hwspec capture -o myspec.json          # JSON (format follows the extension)
hwspec capture -o myspec.yaml
hwspec capture -f text                 # readable summary on stdout
hwspec capture --full -o myspec.json   # include root-only data (asks via polkit)
hwspec capture --redact -o share.json  # strip serials, UUIDs, MACs and hostname
hwspec show myspec.json                # summarise a capture, with refreshed names
hwspec show old.json -o new.json       # re-export a capture with refreshed names
hwspec ids                             # which ID databases are in use
hwspec ids lookup pci 8086:3e92        # resolve one ID
```

### Root access

Most data is readable as a normal user. `--full` re-runs the capture through `pkexec`, which shows your desktop's password prompt (or a terminal prompt over SSH), to add memory modules and serial numbers from the SMBIOS table, and drive health. The output file is still written as your user. Without `--full`, the skipped items are listed under `warnings` and `privileged` is `false`.

## Hardware ID databases

Captures store raw IDs, and names come from these databases. HD Audio codecs need none: the kernel names them, and the codec vendor comes from `pci`.

| Database | Translates | Upstream | Licence |
|---|---|---|---|
| `pci` | PCI vendors, devices, subsystems, classes | [pci-ids.ucw.cz](https://pci-ids.ucw.cz) | GPL-2.0-or-later or BSD-3-Clause |
| `usb` | USB vendors, products, classes | [linux-usb.org](http://www.linux-usb.org/usb-ids.html) | GPL-2.0-or-later or BSD-3-Clause |
| `pnp` | Monitor makers (EDID) | [hwdata](https://github.com/vcrhonek/hwdata) | GPL-2.0-or-later |
| `oui` | MAC address prefixes | [IEEE registry](https://standards-oui.ieee.org/) | public listing |
| `jedec` | Memory makers (JEP106), including codes like `80CE` or HP's `Unknown - [0xF785]` | [i2c-tools](https://git.kernel.org/pub/scm/utils/i2c-tools/i2c-tools.git) `decode-dimms` | GPL-2.0-or-later |
| `amdgpu` | AMD GPU retail names by device and revision | [libdrm](https://gitlab.freedesktop.org/mesa/drm) | MIT |
| `bluetooth` | Bluetooth chip makers (SIG company IDs) | [Bluetooth SIG assigned numbers](https://bitbucket.org/bluetooth-SIG/public) | published by the Bluetooth SIG |
| `cpu` | CPU codename and core microarchitecture by family/model/stepping | Linux kernel [`intel-family.h`](https://github.com/torvalds/linux/blob/master/arch/x86/include/asm/intel-family.h) and [`amd.c`](https://github.com/torvalds/linux/blob/master/arch/x86/kernel/cpu/amd.c), plus [a curated list](tools/genids/cpu-curated.ids) | GPL-2.0 (kernel); curated list GPL-3.0-or-later |

Each database can come from three places, and hwspec uses the **newest**:

- **Embedded** in the binary, so lookups always work offline.
- **Your distro's copy** (`/usr/share/hwdata`, `/usr/share/libdrm`, …), dated by its version header or, failing that, the file's date.
- **Synced** with `hwspec ids update`, into `~/.local/share/hwspec/ids/`.

If the newest copy is unreadable, the next newest is used. Your overrides (below) are applied on top. `hwspec ids` shows what's in use.

### Keeping the databases current

```sh
hwspec ids update --check   # what would change
hwspec ids update           # install the latest databases (~1 MB)
```

A [weekly workflow](.github/workflows/ids.yml) rebuilds all eight databases from upstream, checks each one parses into a plausible number of entries, and publishes them as the [`ids-latest`](https://github.com/jiegui2025/hwspec/releases/tag/ids-latest) release, with a manifest signed by an ed25519 key. `hwspec ids update`:

- checks the manifest's signature against the public key built into hwspec, and refuses a bundle older than the one already installed;
- downloads only the databases that changed, and verifies each one's size, SHA-256 and contents before installing anything;
- installs files atomically, so an interrupted update leaves the previous databases working.

Nothing is sent except the HTTP request itself, and captures never touch the network. Set `HWSPEC_IDS_URL` (or `--url`) to use a mirror of the release files.

### Correcting a name

If a device shows a wrong or missing name, add a line to `~/.config/hwspec/overrides.ids` (`hwspec ids template` prints a commented starting file):

```
pci 8086:3e92 = UHD Graphics 630
usb 046d:c52b = Logitech Unifying Receiver
jedec F785 = Avant Technology
oui 04:0E:3C = HP Inc.
```

Then please submit the correction upstream too (e.g. [pci-ids.ucw.cz](https://pci-ids.ucw.cz) has a web form), so everyone gets it.

## File format

Captures are JSON (or YAML with the same field names). `schema_version` (currently 1) changes only when a field is renamed, removed or changes meaning; new fields can appear without a bump. Sizes are in bytes, temperatures in °C, and other units are named in the field (`_mhz`, `_mts`, `_mbps`). Fields that couldn't be read are omitted or empty. The Go structs in [internal/report/report.go](internal/report/report.go) are the reference.

## Building

```sh
make build        # build/hwspec, static (CGO off)
make test         # vet + unit tests
make release      # amd64 + arm64 tarballs with SHA256SUMS
make update-ids   # refresh the embedded ID databases from upstream
```

| Path | What |
|---|---|
| `cmd/hwspec` | CLI, including the `pkexec` re-run for `--full` |
| `internal/collect` | One collector per area, reading sysfs/procfs; CPU and block devices via [ghw](https://github.com/jaypipes/ghw) |
| `internal/ids` | Layered ID databases, overrides, JEDEC and MAC-prefix decoding |
| `internal/resolve` | Fills in names from raw IDs, for new and saved captures |
| `internal/smbios`, `internal/edid` | SMBIOS (memory modules) and monitor EDID parsers |
| `internal/report`, `internal/output` | The file format, redaction, JSON/YAML/text writers |
| `tools/genids` | Converts upstream ID sources into the embedded files |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development, tests and pull requests, [ARCHITECTURE.md](ARCHITECTURE.md) for how the code is organised, and [SECURITY.md](SECURITY.md) to report a vulnerability. The [project board](https://github.com/users/jiegui2025/projects) tracks the roadmap, defects and maintenance.

## Licence

GPL-3.0-or-later; see [LICENSE](LICENSE). The embedded ID databases keep their own licences, listed above.
