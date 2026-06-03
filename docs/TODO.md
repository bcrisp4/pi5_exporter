# pi5_exporter — TODO / future work

Follow-ups deliberately deferred from the initial CI/CD hardening. The
implemented baseline — modern CI (`actions/*` v6, Node 24), golangci-lint v2,
govulncheck, SHA-pinned actions, Dependabot, least-privilege permissions —
lives under `.github/`.

## Planned

### Release pipeline — GoReleaser + build provenance
A tag-triggered (`v*`) release workflow producing signed, attested, multi-arch
binaries. Research-backed design (June 2026):

- **GoReleaser v2** via `goreleaser/goreleaser-action@v7` with `version: "~> v2"`.
- Build `linux/arm64` (Pi 5 — primary) and optionally `linux/arm` `goarm=7`
  (32-bit Pi OS / Zero 2 W / Pi 3). Pure-Go `CGO_ENABLED=0`, so cross-compiles
  are free.
- Wire the existing `github.com/prometheus/common/version` ldflags
  (`Version={{.Version}}`, `Revision={{.FullCommit}}`, `Branch`, `BuildDate`,
  `BuildUser=goreleaser`) — this feeds `pi5_exporter_build_info`. Use
  `-trimpath` + `mod_timestamp: "{{ .CommitTimestamp }}"` for reproducible builds.
- `checksums.txt` (sha256) + SBOM (`sboms:` via Syft).
- **`actions/attest-build-provenance@v4`** over `dist/checksums.txt` → free,
  Sigstore-signed, SLSA-build-L2-ish provenance, verifiable with
  `gh attestation verify <artifact> --repo bcrisp4/pi5_exporter`.
- Release-job permissions: `contents: write`, `id-token: write`, `attestations: write`.
- Bundle `LICENSE`, `README.md`, and `systemd/` into the release archive.
- Reuse the existing `Makefile` ldflags pattern; validate locally with
  `goreleaser check` / `goreleaser release --snapshot --clean`.

### Container image (wanted soon)
Publish a multi-arch image to `ghcr.io/bcrisp4/pi5_exporter`
(`linux/arm64`, optionally `linux/arm/v7`). For a single static Go binary,
**ko** is the lightest path — no Dockerfile, distroless base, automatic SBOM,
and a GoReleaser `kos:` pipe — preferred over Docker buildx unless a Dockerfile
is specifically required. Attest the image digest as well. The primary
deployment remains the systemd unit; the image is an added convenience.

## Under consideration (not yet decided)
Optional supply-chain hardening beyond the implemented baseline. Worth adopting
if/when chasing an OpenSSF Scorecard badge or OSPS Baseline conformance:

- **CodeQL** code scanning for Go — easiest via repo **Settings → Code security →
  Code scanning → Default setup** (one click, self-updating; no workflow file).
- **Branch protection** on `main` (required status checks: `test`, `lint`; block
  force-push). Deferred because the current flow pushes directly to `main`;
  enabling it moves work to a PR-based flow. Enable with:
  `gh api -X PUT repos/bcrisp4/pi5_exporter/branches/main/protection ...`
- **OpenSSF Scorecard action** (`ossf/scorecard-action`) on a schedule + public badge.
- **zizmor** GitHub Actions static analysis (SARIF to the Security tab).
- **step-security/harden-runner** in `audit` mode (records a CI egress baseline).
- **cosign** keyless signing of release artifacts — largely redundant with the
  build-provenance attestation above.
- **SLSA Build L3** via `slsa-framework/slsa-github-generator` — overkill for a
  small single-binary exporter; only if a downstream consumer requires it.
