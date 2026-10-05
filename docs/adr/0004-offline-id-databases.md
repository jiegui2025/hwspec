# 4. Offline ID databases, newest source wins, signed sync

Status: Accepted (2026-10-05)

## Context

Readable names need ID databases (pci.ids, usb.ids, pnp.ids, IEEE OUI, JEDEC, amdgpu.ids, Bluetooth company IDs, CPU codenames). Distros ship some of them, at different ages and paths, and some not at all. Captures must work offline.

## Decision

- Embed every database in the binary (about 1 MB compressed).
- Per database, use the newest of: embedded copy, distro copy, synced copy. Dates come from manifests, file headers or, failing those, the file's modification time. If the newest copy is unreadable, use the next.
- Apply the user's overrides file last.
- A weekly workflow rebuilds all databases from upstream, deterministically, validates them and publishes a bundle whose manifest is signed with ed25519. `hwspec ids update` verifies the signature against a built-in public key, refuses rollbacks, verifies each file's size, hash and contents, and installs atomically.

## Consequences

- Names work on any system, offline, and improve with distro updates or a sync.
- One trusted publication point; clients don't hit volunteer-run upstream servers.
- The signing key is a long-lived secret: key rotation needs a release that trusts both keys first.
