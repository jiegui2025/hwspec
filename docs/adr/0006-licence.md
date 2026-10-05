# 6. GPL-3.0-or-later

**Status:** Accepted (2026-10-05) · reformatted as tables and diagrams on 2026-10-05, decision unchanged

## Context

| Component | Licence |
|---|---|
| pnp.ids (hwdata), JEDEC table (i2c-tools) | GPL-2.0-or-later |
| CPU codename table | generated from Linux kernel headers (GPL-2.0-only): only model-number ↔ name facts are extracted, no kernel code is distributed |
| IEEE OUI registry | public listing |
| Bluetooth SIG company identifiers | published by the Bluetooth SIG |
| pci.ids, usb.ids | GPL-2.0-or-later or BSD-3-Clause |
| amdgpu.ids (libdrm) | MIT |
| ghw | Apache-2.0 |

## Decision

GPL-3.0-or-later for hwspec.

## Consequences

| ✅ | ⚠️ |
|---|---|
| Compatible with the GPL-2.0-or-later, BSD, MIT and Apache-2.0 components | derivative works must stay open source |
| | the CPU table rests on facts not being copyrightable; if that's ever in doubt, the table can be curated from vendor documentation instead |
| | embedded databases keep their own licences (listed in the README) |
