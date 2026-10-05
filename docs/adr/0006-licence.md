# 6. GPL-3.0-or-later

**Status:** Accepted (2026-10-05)

## Context

| Component | Licence |
|---|---|
| pnp.ids (hwdata), JEDEC table (i2c-tools), kernel-derived CPU data | GPL-2.0-or-later / GPL-2.0 |
| pci.ids, usb.ids | GPL-2.0-or-later or BSD-3-Clause |
| amdgpu.ids (libdrm) | MIT |
| ghw | Apache-2.0 |

## Decision

GPL-3.0-or-later for hwspec.

## Consequences

| ✅ | ⚠️ |
|---|---|
| Compatible with every embedded dataset and dependency, no grey areas | derivative works must stay open source |
| | embedded databases keep their own licences (listed in the README) |
