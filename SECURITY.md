# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities **privately**, not via public issues or pull requests.

- **Preferred:** open a private report through GitHub Security Advisories —
  [Report a vulnerability](https://github.com/bcrisp4/pi5_exporter/security/advisories/new).
- **Alternatively:** email ben@thecrisp.io.

Please include a description, the affected version or commit, and steps to
reproduce. Expect an initial response within a few days.

## Supported versions

This project ships a single binary built from `main`. Fixes land on `main` and
in the next tagged release; please verify against the latest `main` or release
before reporting.

## Scope and operational notes

`pi5_exporter` reads Raspberry Pi 5 firmware telemetry over `/dev/vcio` and
exposes it on an HTTP `/metrics` endpoint. Note:

- The exporter requires membership in the `video` group to access `/dev/vcio`.
  Run it as a dedicated unprivileged user (see `systemd/pi5_exporter.service`).
- `/metrics` is **unauthenticated by default**. Bind it to a trusted interface,
  and/or enable TLS and basic auth via the exporter-toolkit `--web.config.file`
  flag (see the README). Do not expose it directly to untrusted networks.
- The exporter only issues read-only firmware `gencmd` queries; it never writes
  to the firmware mailbox.
