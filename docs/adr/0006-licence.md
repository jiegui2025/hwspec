# 6. GPL-3.0-or-later

Status: Accepted (2026-10-05)

## Context

The binary embeds data under GPL-2.0-or-later (hwdata's pnp.ids, the JEDEC table from i2c-tools, and kernel-derived CPU data) and uses Apache-2.0 code (ghw).

## Decision

License hwspec under GPL-3.0-or-later.

## Consequences

- Compatible with every embedded dataset and dependency without grey areas.
- Derivative works must remain open source. Embedded databases keep their own licences, listed in the README.
