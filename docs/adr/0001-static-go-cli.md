# 1. A static Go binary is the capture engine

Status: Accepted (2026-10-05)

## Context

hwspec must run on NixOS, Arch/CachyOS, Fedora, Ubuntu, Linux Mint, Debian/MX Linux and others, including minimal and musl-based systems. Dynamically linked binaries built elsewhere often fail on NixOS, and interpreters (Python) aren't always installed.

## Decision

Write the engine in Go and build with `CGO_ENABLED=0`, producing one static binary per architecture (amd64, arm64). Use [ghw](https://github.com/jaypipes/ghw) where it is solid (CPU topology, block devices) and our own readers elsewhere.

## Consequences

- One file runs everywhere, verified in CI on six distro images.
- No C libraries: features that need one (GTK, libudev) live outside the engine.
- Go's syscall support covers the ioctls and sockets we need (NVMe admin, Bluetooth management).
