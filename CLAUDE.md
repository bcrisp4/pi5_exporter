# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Prometheus exporter that runs **on a Raspberry Pi 5 (BCM2712)** and exposes
firmware/mailbox telemetry that `node_exporter` cannot reach (PMIC per-rail
power, decoded throttle/under-voltage state incl. the sticky "since boot" bits,
firmware voltages/clocks, SoC/PMIC temperature, RTC backup-cell voltage). It is
deliberately **not another node_exporter** — do not add CPU/mem/disk/net/hwmon/
cpufreq/thermal-zone metrics; those overlap node_exporter on purpose. The only
intentional overlap is SoC temperature. See `README.md` and `docs/metrics.md`.

## Commands

```sh
make build           # static binary (CGO_ENABLED=0) with version ldflags -> ./pi5_exporter
make test            # go test ./...   (the hermetic unit suite)
make lint            # go vet + gofmt check
make run             # build + run on :2712 (needs /dev/vcio + the 'video' group)

go test ./internal/parse/ -run TestParsePMIC   # a single test / package
golangci-lint run                              # full lint (see gotcha below)
go run golang.org/x/vuln/cmd/govulncheck@latest ./...   # reachability vuln scan
```

### Non-obvious gotchas (these will waste your time if you don't know them)

- **`-race` does NOT run on this Pi.** The kernel's 47-bit VMA is incompatible
  with Go's ThreadSanitizer ("unsupported VMA range") — it fails even a trivial
  test. Use plain `go test`. The race detector runs in CI on amd64; that is the
  only place to rely on it. (`make test-race` exists but will fail here.)
- **golangci-lint must be built with Go ≥ the module's `go` directive (1.26).**
  `go install .../golangci-lint@latest` builds it with golangci-lint's own
  pinned Go (1.25) → it then refuses this 1.26 module ("build Go lower than
  targeted"). Force it: `GOTOOLCHAIN=go1.26.4 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`.
  The official release binary the CI action downloads is built with 1.26 and is fine.
- **`go.mod` pins `toolchain go1.26.4`** on purpose — the 1.26.0 stdlib has
  govulncheck-reachable findings that 1.26.4 clears. Keep builds on a patched 1.26.
- **Hardware tests are build-tagged** `//go:build pi5_hardware` (only
  `internal/mailbox/mailbox_hw_test.go`). Run with `make test-hw` **on a real Pi
  5 only** (needs `/dev/vcio` + `video` group). CI never sets the tag, so the
  unit suite stays hermetic — never gate normal tests on hardware.
- **Collector HELP strings are asserted in golden `.prom` fixtures** inside
  `internal/collector/collector_test.go`. Changing a metric's `Help` text means
  updating its golden, or the test fails.

## Architecture (the parts that need several files to grok)

Layered, with side effects pushed to the edges and injected as **small
interfaces declared at the point of consumption** (the dominant design rule):

- **`internal/mailbox`** — the load-bearing `/dev/vcio` transport, and the only
  syscall site. It speaks the VideoCore "gencmd" string protocol over the mailbox
  property tag `0x00030080` (NOT the documented binary tags — one ASCII code path
  for everything, incl. `pmic_read_adc` which has no binary equivalent).
  `BuildGenCmd`/`ParseGenCmdResult` are **pure** (unit-tested with a fake ioctl);
  only `Client.GenCmd`/`Open` touch the kernel. **Read `docs/mailbox.md` before
  changing anything here** — it records how every constant/layout was verified.
- **`internal/parse`** — pure `func(string) (T, error)` parsers, zero I/O. This is
  the bulk of the test surface (table-driven, verbatim captured fixtures).
- **`internal/platform`** — `DetectFamily([]byte)` over `/proc/device-tree/compatible`.
- **`internal/collector`** — one sub-collector per metric group (implements
  `Collector{ Name(); Update() ([]prometheus.Metric, error) }`), plus the master
  `Pi5Collector` (a `prometheus.Collector`) and the `factory` that binds
  `--collector.<name>` flags. Sub-collectors consume the `GenCmder` and
  `FileReader` seams, never `/dev/vcio` or `os` directly.
- **`internal/cache`** — the central runtime model.
- **`main.go`** — flag wiring + the firmware gate (`resolveFirmware`).

### Collect-on-interval / serve-from-cache (the key model)

Hardware is **never read on an HTTP scrape**. A `cache.Scheduler` ticker gathers
an internal registry into an atomically-swapped `[]*dto.MetricFamily` snapshot
every `--collection.interval`; `/metrics` serves that cached snapshot via a
`prometheus.GathererFunc`. An eager first collection runs before the server
starts. `cache_test.go`'s "never collects on scrape" / handler tests are the
regression guard for this — keep them passing. (This is a deliberate deviation
from the usual synchronous-scrape exporter pattern.) Staleness is exposed via
`pi5_exporter_metrics_age_seconds` (computed at serve time, clamped ≥ 0).

### Drop-on-fail

When a collector's `Update` errors, the master **drops** its data series (they go
absent — never replay stale values) and emits `pi5_scrape_collector_success{collector}=0`
plus `..._last_success_timestamp_seconds`. Alert on those meta-metrics, not on
the absence of data series.

### Firmware availability gate (`resolveFirmware` in `main.go`)

`/dev/vcio` openability is the **authoritative** signal. The device-tree
`compatible` string is only a best-effort *negative* check: a board positively
identified as non-BCM2712 (e.g. a Pi 4) is skipped, but an **unreadable**
device-tree is treated as "unknown" and falls through to `/dev/vcio`. This is
why firmware metrics work in a container with just `--device /dev/vcio` even
though podman masks `/sys/firmware` (so `/proc/device-tree` dangles). Do not
re-introduce a hard device-tree gate. The `board` collector still needs the
device tree for `pi5_board_info` (container: `--security-opt unmask=/sys/firmware`).

## Conventions

- Metric names use the `pi5_` prefix and base SI units (`_volts`, `_amperes`,
  `_watts`, `_hertz`, `_celsius`); throttle bits are a labeled state metric
  (`pi5_throttle_state{flag=…}` + `_since_boot`), board identity is an info
  metric (`pi5_board_info{…}=1`).
- Default port `:2712` (BCM2712 mnemonic — the 9100–9999 exporter range is full).
- Releases are tag-driven: pushing a `vX.Y.Z` tag runs `.github/workflows/release.yml`
  (GoReleaser binaries + GitHub release + GHCR image, all attested). Open items
  and the one-time "make the GHCR package public" step are in `docs/TODO.md`.
