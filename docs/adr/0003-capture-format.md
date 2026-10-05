# 3. JSON capture format with raw IDs, additive schema

Status: Accepted (2026-10-05)

## Context

Captures are shared, archived and compared over years, and read by other tools and the planned desktop app.

## Decision

- JSON is the canonical format; YAML is produced from the same structure with the same field names.
- Every named item keeps its raw identifiers (PCI/USB IDs, JEDEC codes, EDID manufacturer IDs, CPU signature) next to the names.
- `schema_version` changes only when a field is renamed, removed or changes meaning. Adding fields is always allowed.
- Unreadable values are omitted or empty, never guessed; the reason goes in `warnings`.
- Units are explicit: bytes, °C, or named in the field (`_mhz`, `_mts`, `_mbps`).

## Consequences

- Old captures can be re-named with newer databases (`hwspec show`).
- Consumers can rely on fields not disappearing within a schema version.
- The structs in `internal/report` are the specification; changes there are reviewed as API changes.
