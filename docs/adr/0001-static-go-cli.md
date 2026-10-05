# 1. A static Go binary is the capture engine

**Status:** Accepted (2026-10-05)

## Context

| Need | Constraint |
|---|---|
| Run on NixOS, Arch/CachyOS, Fedora, Ubuntu, Mint, Debian/MX, Alpine | NixOS rejects dynamically linked foreign binaries; Alpine uses musl |
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
