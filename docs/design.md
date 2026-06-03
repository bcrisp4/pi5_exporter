# pi5_exporter — architecture and design

This document is for maintainers. It explains how the exporter is put together,
why the seams are where they are, and what the invariants are so changes don't
quietly break them. For the wire-level mailbox protocol see
[`mailbox.md`](mailbox.md); this doc covers everything above the transport.

`pi5_exporter` is a Prometheus exporter that runs **on** a Raspberry Pi 5
(BCM2712) and exposes firmware/mailbox telemetry that `node_exporter` does not
cover: PMIC per-rail power, decoded throttle/under-voltage state (including
sticky-since-boot bits), firmware voltages and clocks, SoC/PMIC temperature, and
the RTC backup-cell voltage. The list of metrics is in the README; this doc is
about structure.

The central design choice — and the thing to internalise before changing
anything — is **collect-on-an-interval / serve-from-cache**: hardware is read on
an internal ticker, never on an HTTP scrape. Everything below follows from that.

---

## 1. Package layout and responsibilities

The codebase is a thin pipeline: *read text from hardware → parse it into typed
values → turn values into Prometheus metrics → cache → serve*. Each package owns
exactly one stage, and the impure work is concentrated in as few places as
possible.

```
main.go                       wiring: gate firmware, build collectors, start ticker + HTTP
internal/platform/            pure model/SoC detection from kernel-provided bytes
internal/mailbox/             the ONE syscall site: /dev/vcio gencmd transport
internal/parse/               pure parsers for each gencmd / sysfs text format
internal/collector/           sub-collectors + master collector + flag factory
internal/cache/               ticker + atomic snapshot + serving gatherer
```

- **`internal/parse`** — pure parsers. Callers pass raw command output as a
  string and get back a typed result or an error. No I/O, no globals. One file
  per output format (`measure.go`, `pmic.go`, `throttled.go`, `misc.go`). These
  are the most heavily tested files because they encode all the brittle
  firmware-output knowledge (bit layouts, units, line shapes).

- **`internal/platform`** — pure model/SoC detection. `DetectFamily` decodes the
  NUL-separated `/proc/device-tree/compatible` list (used to gate firmware
  collectors on `brcm,bcm2712`); `DecodeRevision` unpacks a new-style 32-bit
  revision code. It takes bytes/strings, not paths — the caller does the file
  read — so it stays pure and testable against captured fixtures.

- **`internal/mailbox`** — the load-bearing transport and the **single impure
  syscall site** in the whole exporter. On the Pi 5 there is no `/dev/vchiq`;
  `vcgencmd`'s "gencmd" channel is routed through the mailbox property interface
  on `/dev/vcio` via `ioctl(0xC0086400)`. The package marshals a gencmd request
  buffer (`BuildGenCmd`), performs the ioctl, and parses the firmware's reply
  (`ParseGenCmdResult`). Marshalling and parsing are pure; only the ioctl is not,
  and it is isolated behind a seam (see §2).

- **`internal/collector`** — one small `Collector` per logical metric group
  (throttle, pmic, voltage, clock, temperature, board, rtc, watchdog, ringosc,
  reset), the master `Pi5Collector` that runs them, and the `Registry` factory
  that binds the `--collector.<name>` flags. Each sub-collector reads via an
  injected dependency, parses with `internal/parse`, and returns
  `[]prometheus.Metric` **by value** so the master can drop them on failure.

- **`internal/cache`** — the ticker, the atomically-swapped `Snapshot`, the
  `Scheduler` that fills it, and the `Cache.Gather` that serves it. Holds no
  hardware knowledge at all; it just gathers a `Gatherer` on an interval and
  replays the result.

- **`main.go`** — wiring only. Detects the board, gates the firmware, builds the
  enabled collectors, registers them in a **private** registry, runs the eager
  first collection, starts the scheduler goroutine, and serves `/metrics` from
  the cache. It contains no metric logic.

The dependency direction is strictly downward: `collector` depends on `parse`
and on the `GenCmder`/`FileReader` interfaces it declares itself; `cache` depends
on nothing project-specific (just a `Gatherer` interface); `main` depends on all
of them. `parse` and `platform` depend on nothing.

---

## 2. Dependency-injection seams (and why)

The testing strategy is "**pure functions plus a few small interfaces declared at
the point of consumption**". Every place that touches the outside world is named,
typed, and injectable, and the interfaces are deliberately tiny (one or two
methods) so a fake is a couple of lines. The seams:

- **`mailbox.IoctlFunc`** (`func(buf []uint32) error`) — the ioctl seam. The real
  client (`mailbox.Open`) binds it to `realIoctl` over an open `/dev/vcio` fd;
  `mailbox.NewClient(ioctl)` builds a client with `fd = -1` around an injected
  fake. Because the firmware mutates the request buffer in place, a fake ioctl
  just writes a canned response (and `buf[5]`) into the slice it's handed — so the
  full *build → ioctl → parse* path is exercised with no hardware. The injected
  client owns no descriptor, so `Close` is a safe no-op.

- **`collector.GenCmder`** (`GenCmd(cmd string) (string, error)`) — the firmware
  seam, declared in `collector.go` at the point of use (`*mailbox.Client`
  satisfies it). Tests inject `fakeGenCmder`, a `map[string]string` keyed on the
  **exact** command string — so a test simultaneously asserts the command a
  collector sends *and* feeds it canned output. An unrecognised command returns
  an error, catching typos and accidental command changes.

- **`collector.FileReader`** (`func(path string) ([]byte, error)`) — the sysfs /
  device-tree seam. Production passes `os.ReadFile`; tests pass `fakeFS`, an
  in-memory `map[path]contents`. This is how rtc/watchdog/board collectors are
  tested with no real `/sys` or `/proc`.

- **Injected `now func() time.Time`** — threaded into both the `Scheduler` and the
  master `Pi5Collector`. Tests use a fixed clock so timestamps, durations, and
  staleness ages are exact and golden-comparable (e.g. asserting
  `pi5_exporter_metrics_age_seconds == 3`). Production passes `time.Now`.

- **`cache.Gatherer`** (`Gather() ([]*dto.MetricFamily, error)`) — the subset of
  `prometheus.Gatherer` the cache needs, declared in `cache.go`. A
  `*prometheus.Registry` satisfies it in production; a `countingGatherer`
  satisfies it in tests (and counts calls — see §3's guard).

The two principles to preserve when extending: **declare interfaces where they're
consumed, not where they're implemented** (so packages don't take dependencies
they don't need), and **keep the impure surface as a thin injectable boundary
over a pure core**. New firmware metrics should be pure parser + thin collector,
not new syscall sites.

---

## 3. The ticker + cache model

This is the load-bearing architectural decision; read it carefully before
touching `internal/cache` or `main.go`.

### Why

A scrape must be cheap, fast, and side-effect-free. Reading the firmware means
ioctls into the VideoCore mailbox, which we do **not** want to perform once per
Prometheus scrape (and certainly not concurrently per scraper). So we collect on
our own schedule and serve a cached result. Consequences:

- `--collection.interval` (default 15s) sets how often hardware is read.
- The `/metrics` endpoint replays the latest cached snapshot.
- Operators **must** set Prometheus `scrape_interval >= collection.interval`,
  or they'll scrape stale-but-identical data. This is documented on the flag.

### The pieces

- **`Snapshot`** — one immutable collection result: `Families
  []*dto.MetricFamily`, `CollectedAt`, and `Duration`.
- **`Cache`** — an `atomic.Pointer[Snapshot]`. `Store` swaps it; `Load` reads it
  lock-free. Readers (HTTP) and the single writer (ticker) never block each
  other and never see a torn snapshot.
- **`Scheduler`** — holds a source `Gatherer`, the `Cache`, an injected `now`, and
  a logger. `CollectOnce` times a `Gather`, stores a fresh `Snapshot`, and
  returns any gather error. Notably, **a gather error is logged but any partial
  families are still stored** — a broken collector shouldn't blank out the
  healthy ones. `Run(ctx, interval)` calls `CollectOnce` on each tick until the
  context is cancelled.

### Serving: replay + scrape-time staleness meta

`Cache.Gather(now)` is what the HTTP handler calls. It:

1. `Load()`s the snapshot. **If nil (before the first collection) it returns
   `nil, nil`** — an empty scrape rather than an error.
2. Copies the cached families and **appends three staleness meta-metrics computed
   at call time**: `pi5_exporter_metrics_age_seconds` (= `now() − CollectedAt`),
   `pi5_exporter_last_collection_timestamp_seconds`, and
   `pi5_exporter_last_collection_duration_seconds`.
3. Sorts by family name and returns.

The age is computed **at serve time**, not at collection time, so it stays
truthful between ticks (a scrape 12s after a 15s-interval collection reports
`age ≈ 12`, not 0). This is the one bit of per-scrape computation we allow,
because it's pure arithmetic on already-collected data and touches no hardware.

`main.go` wraps this in a `prometheus.GathererFunc` and hands it to
`promhttp.HandlerFor` — so to `promhttp` the cache looks exactly like a normal
gatherer; it just happens to never read hardware.

### The invariant and its guard

**Serving must never trigger a collection.** This is enforced by
`TestServingNeverCollects` (`cache_test.go`): it collects once, calls `Gather`
five times, and asserts the underlying `countingGatherer` was called exactly
once. If a future change makes serving fall through to the live registry, that
test fails. Treat it as a load-bearing regression guard, not a nicety.

### Eager first collection

`main.go` calls `scheduler.CollectOnce()` **before** starting the HTTP server, so
the very first scrape is already populated rather than returning the empty
pre-first-collection result. A startup collection error is logged as a warning
but does not abort boot — a transiently failing collector shouldn't stop the
exporter from coming up and reporting `pi5_scrape_collector_success{...} 0`.

### Concurrency summary

There is exactly one writer (the ticker goroutine, via `CollectOnce` → `Store`)
and many lock-free readers (HTTP handlers, via `Load`). Because `Collect` only
ever runs on the ticker goroutine, the master collector's `lastSuccess` map and
the sub-collectors need no internal locking. Do not call `CollectOnce` or gather
the internal registry from anywhere except the scheduler, or that assumption
breaks.

---

## 4. The master collector

`Pi5Collector` (in `collector.go`) implements `prometheus.Collector` and runs the
enabled sub-collectors. It follows the `node_exporter` pattern.

- **Per-collector timing & meta.** For each sub-collector, `Collect` records
  `start = now()`, calls `Update()`, and emits:
  - `pi5_scrape_collector_duration_seconds{collector}` — always,
  - `pi5_scrape_collector_success{collector}` — `1` on success, `0` on error,
  - `pi5_scrape_collector_last_success_timestamp_seconds{collector}` — emitted
    whenever a prior success timestamp exists (so it can lag the others after a
    failure, which is the point: you can see *when* it last worked).

  These meta-metrics are **always present** regardless of whether the underlying
  data is. `Describe` deliberately sends **only** the three meta descriptors; the
  sub-collectors emit const metrics whose descriptors aren't pre-declared (again
  the node_exporter "unchecked collector" pattern).

- **DROP-ON-FAIL.** When `Update()` returns an error, the master logs it, emits
  `success=0`, and **drops that collector's data series entirely** — they go
  *absent* from the scrape. It does not replay the last good values, and it does
  not emit `0`. This is why `Collector.Update` returns metrics **by value**: the
  master can choose not to forward them.

  Why absent beats the alternatives:
  - **vs. stale-replay:** a replayed value is indistinguishable from a fresh one
    on a dashboard and silently lies. Absent + `success=0` is honest and
    alertable (`pi5_scrape_collector_success == 0`, or `absent(metric)`).
  - **vs. emitting 0:** `0` is a *valid reading* for many of these metrics
    (a gated clock is legitimately `0 Hz`; a throttle flag is legitimately `0`).
    Emitting `0` on failure would be a false "all clear". Absence can't be
    confused with a real value.

  `TestMasterDropOnFail` pins this: an `ok` collector's metric appears, a `fail`
  collector's data is gone, and the `success`/`duration`/`last_success` meta
  reflect both.

The same drop-on-fail philosophy holds at the cache layer (§3): a per-tick gather
error stores whatever partial families came back, so one broken collector never
blanks the healthy ones.

---

## 5. Firmware-availability gate and collector flags

### The gate (in `main.go`)

The firmware (mailbox) collectors require both a BCM2712 board **and** an openable
`/dev/vcio`. `main.go` checks this once at startup, short-circuiting in order:

1. Read `/proc/device-tree/compatible`; on error, warn and disable firmware
   collectors.
2. `platform.DetectFamily(...)`; if `!IsBCM2712`, warn (not a Pi 5) and disable.
3. `mailbox.Open()`; on error, warn and disable. `Open` classifies the error for
   an actionable message: **EACCES → "is this user in the 'video' group?"**
   (`/dev/vcio` is `root:video`); **ENOENT → "not a Pi 5 / firmware too old"**.

Only if all three pass is `firmwareAvailable = true` and the `*mailbox.Client`
used as the `GenCmder`. When false, the firmware collectors are skipped with a
warning **but the sysfs collectors (rtc, watchdog) still run** — the exporter
degrades gracefully on non-Pi-5 hosts and in CI rather than refusing to start.

### Collector flags (the `Registry` factory)

`factory.go` owns the `--collector.<name>` / `--no-collector.<name>` flags. Each
collector is registered with a name, a default-on flag, a `firmware bool`, and a
constructor:

```
add("throttle",    true,  true,  newThrottleCollector)   // firmware, default ON
add("pmic",        true,  true,  newPMICCollector)
add("voltage",     true,  true,  newVoltageCollector)
add("clock",       true,  true,  newClockCollector)
add("temperature", true,  true,  newTemperatureCollector)
add("board",       true,  true,  newBoardCollector)
add("rtc",         true,  false, newRTCCollector)         // sysfs, default ON
add("watchdog",    false, false, newWatchdogCollector)    // sysfs, default OFF
add("ringosc",     false, true,  newRingOscCollector)     // firmware, default OFF
add("reset",       false, true,  newResetCollector)
```

`Registry.Build(deps, firmwareAvailable)` instantiates only the collectors that
are (a) enabled by flag and (b) — if `firmware` — actually backed by available
firmware; firmware collectors are logged and skipped when the gate is closed. The
registry holds **no package-level state**: the flags live on the injected kingpin
application, so the factory is re-entrant and testable. To add a collector:
implement `Collector`, add a `new…Collector` constructor, and add one `add(...)`
line — nothing else changes.

---

## 6. Testing strategy

The architecture exists to make this fast and hermetic. `make test`
(`go test ./...`) runs entirely without hardware.

- **Pure-parser table tests** (`internal/parse/parse_test.go`,
  `internal/platform/platform_test.go`). The brittle firmware/kernel knowledge
  lives in pure functions, so it's tested with ordinary table-driven cases over
  captured live fixtures — including malformed-input and command-level-error
  cases. This is where bit layouts, units, and line shapes are nailed down.

- **Golden `.prom` collector tests** (`collector_test.go`) via
  `prometheus/client_golang/prometheus/testutil`. Each collector is driven with a
  `fakeGenCmder` (exact-command keyed) and/or `fakeFS`, then
  `CollectAndCompare`/`GatherAndCompare` checks the emitted metrics byte-for-byte
  against an inline golden exposition block. These tests assert names, labels,
  HELP/TYPE, values, and the *exact command strings sent* — so a renamed metric,
  a changed label, or a typo'd gencmd all fail loudly.

- **Cache tests** (`cache_test.go`) with a `countingGatherer` and a fixed clock:
  snapshot storage, scrape-time staleness math, the empty-before-first-collection
  case, and the `TestServingNeverCollects` guard (§3).

- **Master-collector test** (`TestMasterDropOnFail`): proves drop-on-fail and the
  meta-metric bookkeeping (§4).

- **Hardware integration test** (`internal/mailbox/mailbox_hw_test.go`), guarded
  by `//go:build pi5_hardware`. CI never sets the tag, so it never compiles
  there; on a real Pi 5 in the `video` group `make test-hw` exercises the live
  `/dev/vcio` ioctl end to end (build request → real ioctl → parse) by reading
  `measure_temp` and sanity-checking the value. It `t.Skip`s cleanly if
  `/dev/vcio` is absent or unopenable, so it's safe to invoke on a non-Pi.

### The `-race` caveat

`go test -race` **cannot run on this Pi 5**: the ThreadSanitizer runtime assumes a
48-bit virtual-address space, but this kernel exposes a 47-bit VMA, so a `-race`
binary aborts at startup. Therefore:

- **CI runs `-race` on amd64**, where the address-space assumption holds. That's
  where the concurrency guarantees (single-writer ticker, lock-free readers) are
  actually race-checked.
- **On a Pi**, run the hermetic suite (`make test`) and the hardware suite
  (`make test-hw`) **without** `-race`. The `test-hw` Make target currently passes
  `-race`; drop that flag when invoking on the Pi itself (the firmware path it
  covers is single-goroutine anyway).

The takeaway: hardware-path correctness is verified on the Pi; data-race freedom
is verified in amd64 CI. Neither environment alone covers both.
