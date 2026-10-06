# 1. A static Go binary is the capture engine

**Status:** Accepted (2026-10-05) · reformatted as tables and diagrams on 2026-10-05, decision unchanged · amended 2026-10-06: block devices read by hwspec, not ghw ([#142](https://github.com/jiegui2025/hwspec/issues/142))

## Context

| Need | Constraint |
|---|---|
| Run on NixOS, Arch/CachyOS, Fedora, Ubuntu, Mint, Debian/MX, Alpine | dynamically linked binaries built elsewhere often fail on NixOS (no standard loader path); Alpine uses musl |
| Zero setup for users | Python and other interpreters aren't always installed |
| Low-level access (ioctls, sockets) | must not need C libraries |

## Options

| Option | One file for every distro | Low-level access | Effort |
|---|---|---|---|
| **Go, `CGO_ENABLED=0`** | ✅ static | ✅ `syscall`, `x/sys/unix` | low; [ghw](https://github.com/jaypipes/ghw) covers CPU and block devices |
| Rust, musl target | ✅ static | ✅ | higher: no ghw equivalent |
| Python | ❌ needs an interpreter | ⚠️ ctypes | low |

## Decision

Go, built with `CGO_ENABLED=0`, one static binary per architecture (amd64, arm64). Use ghw where it is solid, our own readers elsewhere.

## Consequences

| ✅ | ⚠️ |
|---|---|
| One file runs everywhere; CI proves it on six distro images | features needing C libraries (GTK, libudev) live outside the engine |
| Syscall support covers the NVMe admin ioctl and Bluetooth management socket | we maintain the readers ghw doesn't provide |

## Amendment (2026-10-06): block devices read by hwspec, not ghw

ghw's block reader turned out not to be solid for a capture format: values came out altered, and its reads bypass the collectors' file layer ([#142](https://github.com/jiegui2025/hwspec/issues/142), from the architecture review [#132](https://github.com/jiegui2025/hwspec/issues/132)). The owner chose option A, an own reader, for v0.1.0.

| Fact | Source |
|---|---|
| Every value went through `clean()`, which turned `_` into a space: `SYSTEM_DRV` → `SYSTEM DRV`, `crypto_LUKS` → `crypto LUKS` | `internal/collect/storage.go` before #142; a loop disk captured with both builds (#142's PR) |
| A partition's `uuid` held the GPT partition UUID (udev `ID_PART_ENTRY_UUID`), not the filesystem UUID that `lsblk`'s UUID column, `/etc/fstab` and `blkid` mean | ghw `pkg/block/block_linux.go` `diskPartUUID` |
| ghw's reads don't go through `readFile`, so `Paths` couldn't trace them, recordings couldn't replay a failing read, and `tools/snapshot` needed a list of files ghw's clone misses | `internal/collect/recorded.go`, `tools/snapshot` |

| Aspect | Rule |
|---|---|
| Block devices | `internal/collect/block.go` reads `/sys/block`, `/run/udev/data/b<major:minor>` and `/proc/self/mounts` through `readFile`, like every other collector |
| Values | as recorded, never re-spelled: udev's exact `_ENC` form where one exists (`ID_FS_LABEL_ENC`, `ID_MODEL_ENC`, `\xNN` decoded), else the plain property; the filesystem type from udev, else the mount table |
| `uuid`, `partuuid` | `uuid` is the filesystem UUID (udev `ID_FS_UUID`); the new `partuuid` is the partition table entry's (udev `ID_PART_ENTRY_UUID`, else the kernel's uevent). Both are removed by `--redact` |
| ghw | still reads the CPU topology; replacing that too, and dropping ghw, is a later step |
