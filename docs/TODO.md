# pi5_exporter — status & future work

## Implemented

- **CI** (`.github/workflows/ci.yml`): modern actions (v6, Node 24), `test` +
  `lint` jobs, `-race`/`-shuffle`/coverage + arm64 cross-compile, golangci-lint v2
  (`.golangci.yml`) and govulncheck. SHA-pinned actions + Dependabot
  (`github-actions` + `gomod`), least-privilege permissions, `SECURITY.md`.
- **CodeQL** code scanning for Go (default setup, enabled in repo settings).
- **Container image** (`Dockerfile` + `.dockerignore`): multi-stage, distroless
  `static-debian12:nonroot`, ~15 MB, version ldflags wired in. Verified on a Pi 5.
- **Release pipeline** (`.github/workflows/release.yml` + `.goreleaser.yaml`):
  on a `v*` semver tag it builds the binaries (linux/arm64 + armv7), archives
  (with `LICENSE`/`README`/`systemd`), `checksums.txt`, an SBOM, and a GitHub
  Release via GoReleaser; builds and pushes the `linux/arm64` image to
  `ghcr.io/bcrisp4/pi5_exporter`; and signs build-provenance attestations for
  both (verifiable with `gh attestation verify`).

### Cutting a release
Push a semver tag, e.g.:
```sh
git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```
After the first image push, make the GHCR package **public** in its settings if
you want unauthenticated `podman pull` (it defaults to private).

## Possible future work

- **armv7 container image** — the image is currently `linux/arm64` only (binaries
  already cover armv7). Adding `linux/arm/v7` needs `GOARM` handling from
  `TARGETVARIANT` in the Dockerfile + a multi-platform `build-push-action`.
- **Branch protection** on `main` (required checks: `test`, `lint`; block
  force-push). Deferred because the current flow pushes directly to `main`.
  `gh api -X PUT repos/bcrisp4/pi5_exporter/branches/main/protection ...`
- **OpenSSF Scorecard** action + public badge; **zizmor** workflow static
  analysis (SARIF to the Security tab); **step-security/harden-runner** in
  `audit` mode. Adopt if chasing a Scorecard badge / OSPS Baseline conformance.
- **cosign** keyless signing of release artifacts — largely redundant with the
  build-provenance attestations already produced.
- **SLSA Build L3** via `slsa-framework/slsa-github-generator` — overkill for a
  small single-binary exporter unless a downstream consumer requires it.
