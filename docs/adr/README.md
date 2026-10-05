# Architecture decision records

Each record captures one significant decision as context → options → decision → consequences, mostly as tables and diagrams. Records aren't edited after acceptance except to mark them superseded; a changed decision gets a new record.

| # | Decision | Status |
|---|---|---|
| [0001](0001-static-go-cli.md) | A static Go binary is the capture engine | Accepted |
| [0002](0002-kernel-interfaces.md) | Read kernel interfaces directly, not other tools | Accepted |
| [0003](0003-capture-format.md) | JSON capture format with raw IDs, additive schema | Accepted |
| [0004](0004-offline-id-databases.md) | Offline ID databases, newest source wins, signed sync | Accepted |
| [0005](0005-privileged-rerun.md) | Root access by re-running under pkexec, no daemon | Accepted |
| [0006](0006-licence.md) | GPL-3.0-or-later | Accepted |
| [0007](0007-desktop-ui.md) | Desktop app in Rust with GTK4 and libadwaita, driving the CLI | Proposed |

New records: copy the structure of an existing one, take the next number, prefer tables and Mermaid diagrams to prose.
