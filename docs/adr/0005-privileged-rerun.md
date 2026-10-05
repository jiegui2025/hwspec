# 5. Root access by re-running under pkexec, no daemon

Status: Accepted (2026-10-05)

## Context

Memory modules (SMBIOS), serial numbers and drive health need root. Tools like Chassis install a resident root daemon on D-Bus; that is more surface area and needs systemd integration that MX Linux (sysvinit) lacks.

## Decision

`hwspec capture --full` re-runs the same binary under `pkexec` for a one-shot capture to stdout. The unprivileged parent parses it, applies the user's overrides and writes the file as the user.

## Consequences

- Nothing runs as root except for the seconds of a capture, and only when asked.
- Works with any polkit agent, including the terminal agent over SSH; `sudo hwspec capture` also works.
- No live root-only features (e.g. driver binding) without revisiting this decision.
