# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through [GitHub security advisories](https://github.com/jiegui2025/hwspec/security/advisories/new), not as public issues. You'll get a reply within a week. Fixes are released as soon as they're ready, and the advisory is published with credit unless you prefer otherwise.

Only the latest release is supported.

## What's in scope

hwspec reads hardware information and, with `--full`, runs as root through `pkexec`. The areas most worth scrutiny:

- The privileged re-run (`--full`): what runs as root and what it reads.
- `hwspec ids update`: download, signature and checksum verification, and installation of ID databases.
- Parsing of untrusted data: captures given to `hwspec show`, ID database files, EDID and SMBIOS tables.

## Verifying downloads

Release binaries come with `SHA256SUMS` and a build provenance attestation:

```sh
gh attestation verify hwspec-v0.1.0-linux-amd64.tar.gz --repo jiegui2025/hwspec
```

ID database bundles are signed with an ed25519 key whose public half is built into hwspec (`internal/ids/key.go`); `hwspec ids update` refuses anything not signed by it.
