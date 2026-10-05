# 2. Read kernel interfaces directly, not other tools

Status: Accepted (2026-10-05)

## Context

Tools like `lshw`, `dmidecode`, `lspci`, `inxi` and `hwinfo` aren't installed everywhere, differ in versions and output, and parsing their text output is brittle.

## Decision

Read `/sys`, `/proc`, the raw SMBIOS table, EDID blobs, the NVMe admin ioctl and the Bluetooth management socket directly. The one exception is `smartctl` for SATA/USB drive health, used only when installed, because ATA SMART pass-through is large and device-specific.

## Consequences

- Same results on every distro; no runtime dependencies.
- We own the parsers (SMBIOS, EDID, NVMe SMART, mgmt replies), so they must be well tested with byte fixtures.
- New hardware classes need new readers rather than a new tool invocation.
