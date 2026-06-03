# pi5_exporter — Implementation Plan

## Context

Raspberry Pi 5 (Broadcom BCM2712) exposes a class of telemetry that **no widely-used
exporter surfaces**: the VideoCore firmware's per-rail PMIC power (volts **and** amps →
watts), the decoded under-voltage/throttle state (live **and** sticky-since-boot), firmware
core/SDRAM voltages, the VideoCore-domain clocks, and the Pi 5 RTC backup-cell voltage.
node_exporter cannot reach any of this — it lives behind the firmware mailbox.

Earlier in this project we **proved on this exact board** how to read it: Pi 5 has no
`/dev/vchiq`; `vcgencmd` routes every command over the mailbox property interface via
`ioctl(/dev/vcio, 0xC0086400)` using the gencmd tag `0x00030080`. We reproduced this natively
(Python `ioctl`) and got byte-identical output to `vcgencmd`, including the full
`pmic_read_adc` dump. So a pure-Go exporter (no cgo, no `vcgencmd` dependency) is feasible.

**Outcome:** a small, well-tested Go Prometheus exporter, `pi5_exporter`, that collects
Pi-5-specific firmware metrics on an internal interval, caches them, and serves the cached
snapshot at `/metrics`. It deliberately does **not** duplicate node_exporter; it complements
it. Built test-first (red/green/refactor), with the load-bearing mailbox protocol and all
tag/string provenance documented.

This plan reflects a 3-perspective design pass + an adversarial critique, plus two
correctness questions settled by live hardware probes this session.

---

## Decisions (locked)

- **Module:** `github.com/bcrisp4/pi5_exporter` · **License:** Apache-2.0 · **Go:** 1.26 · **arm64**.
- **Transport:** gencmd-string-only over `/dev/vcio` tag `0x00030080`. Binary property tags are
  **documented for provenance but NOT implemented** (one ASCII code path; PMIC has no binary
  equivalent anyway; simplest + most testable).
- **Architecture:** internal ticker collects every `--collection.interval`; `/metrics` serves a
  cached `[]*dto.MetricFamily` snapshot. Hardware is **never** read on an HTTP scrape.
- **Throttle schema:** labeled — `pi5_throttle_state{flag=…}` + `pi5_throttle_state_since_boot{flag=…}`.
- **Default-ON collectors:** `throttle`, `pmic`, `voltage`, `clock`, `temperature` (SoC **and**
  PMIC die temp), `rtc`, `board`. **Default-OFF (built):** `watchdog`, `ring_osc`, `reset_cause`.
- **Dropped:** `power_monitor` (needs start/stop trace args — not a snapshot reader).
- **Port:** `:9110` default (verify it's free on the Prometheus default-port wiki before release).

---

## Goal & scope — what's in, what's out

**In (Pi-5-specific, firmware/mailbox + a little sysfs):** throttle/under-voltage decode, PMIC
per-rail power, core/SDRAM voltages, VideoCore clocks, SoC+PMIC temperature, RTC backup-cell
voltage, board identity, and the off-by-default diagnostics (watchdog reset status, ring
oscillator, reset cause).

**Out (node_exporter already covers — we do NOT rebuild):** generic `hwmon` (`rpi_volt`
under-voltage *alarm*, `pwmfan`, `nvme`, `rp1_adc`), `thermal_zone` plumbing, ARM `cpufreq`,
CPU/mem/disk/net. (We *do* emit SoC temp via firmware because a standalone user expects it; it
overlaps `thermal_zone0` but is a basic, low-cost metric.)

The firmware/mailbox collectors are gated on `/proc/device-tree/compatible` containing
`brcm,bcm2712`. The sysfs collectors (`rtc`, `watchdog`) are gated on their sysfs path existing,
**not** on the SoC (a Pi-5 RTC path simply won't exist elsewhere).

---

## Architecture & data flow

```
pi5_exporter/
  main.go                         # thin wiring only (~60 lines): flags → object graph → serve
  go.mod  go.sum  LICENSE  README.md  Makefile
  systemd/pi5_exporter.service
  docs/{mailbox.md, metrics.md, design.md}
  internal/
    parse/      # PURE parsers: throttled, pmic, volts, clock, version, temp, sysfs ints
    platform/   # PURE model detection over device-tree compatible bytes + revision fallback
    mailbox/    # the ONE syscall site: pure buffer build/parse + thin injected-ioctl Client
    collector/  # one file per collector + master Pi5Collector + factory/flags
    cache/       # ticker + atomic snapshot (the async architecture)
    testdata/   # shared verbatim vcgencmd captures (embedded)
```

**Seam interfaces are declared at the point of consumption** (in `collector`, `cache`,
`mailbox`), never in the implementing package. Side effects (ioctl, file reads, clock) are
injected as small interfaces or function values.

**Collection / serving flow** (resolves the critique's cache conflict — single model):

```
sub-collector.Update() ([]prometheus.Metric, error)        # reads HW via injected GenCmder/FileReader
        │  (by-value return → enables clean drop-on-fail)
Pi5Collector (implements prometheus.Collector):            # node_exporter-style master
   for each ENABLED sub:
       t0=now(); ms,err=sub.Update(); dur=now()-t0
       emit pi5_scrape_collector_duration_seconds{collector}=dur
       if err: emit pi5_scrape_collector_success{collector}=0; log; DROP ms   # drop-on-fail → series absent
       else:   emit pi5_scrape_collector_success{collector}=1; lastSuccess[collector]=now(); forward ms
       emit pi5_scrape_collector_last_success_timestamp_seconds{collector}=lastSuccess[collector]  # ALWAYS present
internalRegistry = NewRegistry(); register(Pi5Collector, versioncollector)
        │
Scheduler (ticker, interval): fams,_=internalRegistry.Gather()
   stamp fams += last_collection_timestamp & last_collection_duration
   cache.Store(Snapshot{Families:fams, CollectedAt:now()})     # eager run ONCE before serving
        │
servingGatherer (GathererFunc): snap=cache.Load(); out=clone(snap.Families)
   out += pi5_exporter_metrics_age_seconds = now()-snap.CollectedAt   # computed at scrape time
   return sorted(out)
        │
promhttp.HandlerFor(servingGatherer, {ErrorHandling:ContinueOnError, MaxRequestsInFlight, Registry})
   → /metrics      (+ exporter-toolkit landing page at /)
```

Consequences (all from the adversarial critique, accepted):
- **No `pi5_up`** (would collide with Prometheus's own `up`). Health is per-collector
  `pi5_scrape_collector_success` + staleness via `pi5_exporter_metrics_age_seconds` /
  `pi5_exporter_last_collection_timestamp_seconds`.
- **Drop-on-fail, not stale-replay:** a collector that errors on a tick has its data series go
  *absent* (Prometheus writes a staleness marker; graphs gap) rather than silently replaying
  frozen values — a stale `pi5_pmic_rail_watts` / `pi5_throttle_state` during a real power or
  throttle event would mislead exactly when the truth matters most. The failure is signalled by
  **always-present** meta-metrics, never by the absence itself: `pi5_scrape_collector_success
  {collector}=0` **plus** `pi5_scrape_collector_last_success_timestamp_seconds{collector}` (so
  `time() - …last_success…` gives a per-collector staleness age you can graph/alert on even
  while the data series is gone). Alerts target those, not `absent()` of the data. Never emit
  `0` for a failed read (0 V / 0 W reads as a real measurement). This is the node_exporter
  pattern and matches Prometheus's "writing exporters" guidance on partial-failure exposure.
- **Eager first gather happens in `main` BEFORE `web.ListenAndServe`** so the first scrape is
  never empty (no goroutine race).
- **`now` and the ticker are injected** → cache/scheduler tests use a fake clock + manual
  `Tick()`, zero real `time.Sleep`, deterministic.

---

## Load-bearing detail: the mailbox transport (`internal/mailbox`)

Only the ioctl is impure. Everything else is pure and unit-tested with zero hardware.

**Constants (with provenance comments in code, full write-up in `docs/mailbox.md`):**
- `ioctlMboxProperty = 0xC0086400` — `_IOWR('d'=100,0,char*)`; arm64 `dir(3)<<30|size(8)<<16|100<<8|0`.
  Verified: recomputed on-board + matches our `strace` of `vcgencmd`.
- `tagGetGencmdResult = 0x00030080` — `GET_GENCMD_RESULT`. **Absent from the kernel header**
  (kernel never issues gencmd); defined in `raspberrypi/utils vcgencmd.c`. Verified via strace.
- `maxString = 4096` — value-buffer size (`MAX_STRING` in `vcgencmd.c`).
- Status words `RPI_FIRMWARE_STATUS_SUCCESS=0x80000000 / _ERROR=0x80000001` (kernel header).

**Request buffer (`[]uint32` LE), built by pure `BuildGenCmd(cmd) []uint32`:**
`[0]`=total bytes (set last) · `[1]`=0 process-request · `[2]`=0x00030080 · `[3]`=4096 ·
`[4]`=0 req-len · `[5]`=0 retcode slot · `[6..]`=NUL-terminated command string · advance 1024
words · `[end]`=0. Size = 6 + 1024 + 1 = 1031 words.

**Response, pure `ParseGenCmdResult(buf) (string,error)`:** if `buf[5]!=0` → hard error
(`ErrGencmdRetcode{Code,Body}`); else return value region `[6..]` truncated at first NUL.

**Error contract — settled by live probe this session (this resolves the biggest open question):**

| Command | `buf[5]` | ASCII body | Interpretation |
|---|---|---|---|
| `measure_volts core` | `0x00000000` | `volt=0.8749V` | success |
| `measure_clock h264` | `0x00000000` | `frequency(0)=0` | success, value 0 (idle/gated — valid) |
| `measure_volts bogus` | `0x00000000` | `bad argument` | **command-level error, retcode still 0** |
| `not_a_real_command` | `0xffffffff` | `error=1 error_msg="…"` | unknown command → retcode set |

→ Transport surfaces the raw body when `buf[5]==0`. **Each strict parser rejects anything not
matching its exact format** (`volt=`, `frequency(…)=`, `throttled=0x…`), so `bad argument` /
`error=…` become parse errors automatically — **no special-case string sniffing**. Collectors
send **exact constant command strings** (a typo'd `measure_volts` silently returns *core*
voltage), and tests assert the exact command string sent.

**Client + injected seam (the only `os`/syscall code):**
```go
type ioctlFunc func(buf []uint32) error        // declared here, real impl wraps unix.Syscall(SYS_IOCTL,…)
type Client struct{ ioctl ioctlFunc }
func (c *Client) GenCmd(cmd string) (string,error)   // build → c.ioctl(buf) → parse
func Open() (*Client, error)                          // os.OpenFile("/dev/vcio") + real ioctl
```
- `Open()` errors are classified with `errors.Is`: `fs.ErrPermission`/`EACCES` → "is the user in
  the `video` group?"; `ENOENT` → "no `/dev/vcio` (not a Pi 5 / firmware too old)".
- The fake ioctl in tests is a ~15-line closure that writes a canned response — **not** a
  VideoCore emulator. `Open()` and the real syscall are exercised **only** by the build-tagged
  hardware test.

(Cut from the original design per critique: the separate `internal/vcgencmd` Runner wrapper and
the separate `opener` seam — both unnecessary layers. Collectors consume a tiny
`GenCmder interface{ GenCmd(string)(string,error) }` that `*mailbox.Client` satisfies directly.)

---

## Metric schema

Prefix `pi5_`, base units, all `GaugeValue`, custom registry, `MustNewConstMetric`.

| Metric | Labels | Unit | Collector | Default |
|---|---|---|---|---|
| `pi5_throttle_state` | `flag`={under_voltage,arm_frequency_capped,throttled,soft_temp_limit} | 0/1 live | throttle | ON |
| `pi5_throttle_state_since_boot` | `flag`=(same 4) | 0/1 sticky | throttle | ON |
| `pi5_throttle_flags` | — | raw int (debug) | throttle | ON |
| `pi5_pmic_rail_volts` | `rail` | volts | pmic | ON |
| `pi5_pmic_rail_amperes` | `rail` | amperes | pmic | ON |
| `pi5_pmic_rail_watts` | `rail` | watts (V×A, rails with both) | pmic | ON |
| `pi5_pmic_measured_power_watts` | — | watts (Σ measured rails; **not** board input power) | pmic | ON |
| `pi5_voltage_volts` | `domain`={core,sdram_c,sdram_i,sdram_p} | volts | voltage | ON |
| `pi5_clock_hertz` | `domain`={arm,core,v3d,isp,h264,hevc,pixel,hdmi,emmc,uart,pwm,vec,dpi} | hertz | clock | ON |
| `pi5_soc_temperature_celsius` | — | °C | temperature | ON |
| `pi5_pmic_temperature_celsius` | — | °C | temperature | ON |
| `pi5_rtc_battery_volts` | — | volts | rtc | ON |
| `pi5_rtc_charging_volts` `_min` `_max` | — | volts | rtc | ON |
| `pi5_board_info` | `model,revision,serial,firmware_hash,firmware_variant,soc` | const 1 | board | ON |
| `pi5_watchdog_bootstatus` / `_timeout_seconds` | — | int / s | watchdog | OFF |
| `pi5_ring_osc_hertz` | — | hertz | ring_osc | OFF |
| `pi5_reset_status` | — | raw int (get_rsts) | reset_cause | OFF |
| **Meta:** `pi5_exporter_build_info` | version,revision,goversion | 1 | (always) | — |
| `pi5_scrape_collector_success` / `_duration_seconds` / `_last_success_timestamp_seconds` | `collector` | 0/1 · s · unixtime | (master) | — |
| `pi5_exporter_last_collection_timestamp_seconds` / `_duration_seconds` / `pi5_exporter_metrics_age_seconds` | — | s | (cache) | — |

Notes (documented in HELP / `docs/metrics.md`): PMIC power sum **excludes** volt-only `EXT5V`/
`BATT` and is the sum of independently-measured rails, *not* total board input power (HDMI is a
5V rail and is correctly included). Clock `0` = domain idle/clock-gated, not broken. RTC
`charging_volts=0` = trickle charge disabled / no cell; `_min`/`_max` show the configurable
window. PMIC rails keyed by **name** (indices are non-sequential), `_A`/`_V` merged.

---

## TDD plan — ordered red/green/refactor (strictly bottom-up)

Each layer is fully testable before the next exists. Pure code first; side effects last.

1. **Pure parsers** (`internal/parse`) — `Throttled`, `PMIC`, `Volt`, `Clock`, `Version`,
   `Temp`, `MicrovoltsFromSysfs`. RED: table-driven `t.Run` tests using the **verbatim captured
   fixtures** (e.g. `throttled=0x0`, `throttled=0x50005`; the full 26-line PMIC dump incl.
   leading whitespace + volt-only `EXT5V`/`BATT`; clocks incl. `=0`; `bad argument` → error).
   GREEN: minimal `strings`/`strconv`/`regexp`. REFACTOR: shared helpers; provenance comments.
2. **Model detection** (`internal/platform`) — `DetectFamily(compatible []byte)` (NUL-split,
   look for `brcm,bcm2712`) + `DecodeRevision(hex)` fallback. Pure over byte literals.
3. **Mailbox framing** (`internal/mailbox`) — `BuildGenCmd`/`ParseGenCmdResult`. RED asserts
   exact word layout + retcode handling + round-trip through a fake-firmware echo. **Zero ioctl.**
4. **`Client.GenCmd`** — inject fake `ioctlFunc` that validates the inbound buffer and writes a
   canned response; assert parsing + `buf[5]!=0` error + `EACCES` wrapping.
5. **Each collector** (`internal/collector`) — inject `fakeGenCmder` (map cmd→canned output)
   / in-memory `FileReader`; assert exposition via `testutil.CollectAndCompare` against
   `testdata/*.prom` golden, plus `testutil.CollectAndLint` (promlint) gate. Order: throttle →
   pmic (incl. derived watts + measured-power sum) → voltage → clock → temperature → rtc →
   board → watchdog/ring_osc/reset_cause.
6. **Master `Pi5Collector`** — enable/disable + per-collector `success`/`duration`; assert a
   failing sub drops its data metrics but still emits `success=0` + a present
   `last_success_timestamp` (drop-on-fail); disabled sub emits nothing.
7. **Cache + ticker** (`internal/cache`) — inject fake `Gatherer` + fake ticker + `now`. Assert:
   eager snapshot before first tick is populated; `Tick()` refreshes; `-race` concurrent
   readers stay consistent; gather error keeps last snapshot but surfaces it.
8. **HTTP handler** — `httptest`; assert 200 + cached body, and the **key regression test
   `TestMetricsHandlerNeverCollectsOnScrape`**: a spy gatherer proves sub-collector `Update`
   runs **0** times across N scrapes (only ticks collect). This guards the cache requirement.
9. **Thin `main` flag wiring** — pure `Enabled() map[string]bool` resolution tested over arg
   vectors (`--no-collector.pmic`, `--collector.disable-defaults --collector.throttle`); `main`
   itself stays ≤~60 lines (no unit test beyond flag resolution + the hardware test).

**Hardware integration test** (`//go:build pi5_hardware`): opens real `/dev/vcio`, round-trips
`GenCmd("measure_temp")`, asserts a sane range. CI never sets the tag → hermetic. Run on this
board via `make test-hw`. This is the only place `Open()` + the real syscall are exercised.

**Test conventions:** table-driven + `t.Run`; `go test -race ./...`; tiny hand-written fakes
(no mock library); golden `.prom` + embedded `testdata`; promlint enforced in collector tests.

---

## Dependencies (minimal, justified)

| Module | Why |
|---|---|
| `prometheus/client_golang` | required: registry, `MustNewConstMetric`, `promhttp`, `testutil`. |
| `prometheus/exporter-toolkit` | `web` (listen, TLS, basic-auth via `--web.config.file`), landing page, `kingpinflag`. Don't reinvent TLS. |
| `alecthomas/kingpin/v2` | node_exporter-style `--collector.x`/`--no-collector.x` flags. |
| `prometheus/common` | `version` (`*_build_info`, `--version`) + `promslog` (`--log.level/format`). |
| `golang.org/x/sys` | `unix` ioctl for `/dev/vcio` (frozen `syscall` is not the maintained path). |

**Not added:** no web framework (stdlib `net/http`+promhttp), no YAML/config lib, no mock lib,
no `go-cmp` (use `reflect.DeepEqual` + a 5-line `diffRails` helper), no `procfs`/`gopsutil`
(that's node_exporter's job). Direct deps ≈ 5.

---

## Flags (kingpin, no globals)

A `Factory` registers each collector's name + default-enabled + constructor and binds
`--collector.<name>` (kingpin gives `--no-collector.<name>` free). `Enabled()` returns the
resolved set after `Parse` (pure, unit-tested). Plus: `--collector.disable-defaults`
(node_exporter parity), `--collection.interval` (default `15s`), `--web.listen-address` /
`--web.config.file` (via `kingpinflag.AddFlags`, default `:9110`), `--log.level`/`--log.format`
(promslog), `--version`.

---

## Packaging & docs

- **README.md:** what it is / is **not** (table of each metric vs the node_exporter feature it
  complements); requirements (Pi 5/BCM2712; **user must be in `video` group** — the #1 gotcha);
  install/run; flags table; **cache/interval semantics** (set Prometheus `scrape_interval ≥
  collection.interval`; staleness via `pi5_exporter_metrics_age_seconds`); example `scrape_config`;
  example PromQL/alerts (under-voltage now + since-boot, measured power, RTC cell low);
  troubleshooting. Links to official Prometheus docs for readers newer to exporters.
- **docs/mailbox.md** (load-bearing provenance): strace finding (no `/dev/vchiq` on Pi 5); the
  ioctl derivation `0xC0086400`; gencmd tag `0x00030080` + `MAX_STRING`; the word-by-word buffer
  layout table; the **error contract table above**; the gencmd command-string table with one
  verbatim fixture each; the binary property-tag reference table from the kernel header (marked
  *not implemented*, with the reason); the byte-identical Python reproduction evidence; throttle
  bit semantics; model gating. Sources: `raspberrypi/utils` `vcgencmd.c`+`mailbox.c`; kernel
  header path `…/raspberrypi-firmware.h`; raspberrypi.com vcgencmd/revision-code docs; our
  on-hardware strace + repro.
- **docs/metrics.md** — every metric: name, unit, source command, node_exporter delineation.
- **docs/design.md** — the ticker+cache architecture & rationale, DI seams, drop-on-fail.
- **systemd/pi5_exporter.service** — dedicated `pi5_exporter` user, `SupplementaryGroups=video`,
  `NoNewPrivileges`, `ProtectSystem=strict`, `DeviceAllow=/dev/vcio rw`, `MemoryMax=64M`.
- **Makefile** — `build` (version ldflags → `pi5_exporter_build_info`), `test` (`-race`),
  `lint`, `test-hw` (`-tags pi5_hardware`), `build-arm64`. **CI:** hermetic (vet, `test -race`,
  gofmt, promlint, cross-compile `GOARCH=arm64`); hardware tag only via `make test-hw`.

---

## Critical files

- `internal/mailbox/buffer.go` — pure gencmd buffer build/parse (must match the verified layout).
- `internal/mailbox/client.go` — `GenCmd` + injected `ioctlFunc` + real `Open()` (hardware-test-only).
- `internal/parse/pmic.go` — PMIC parser feeding the crown-jewel power collector.
- `internal/collector/collector.go` — `GenCmder`/`FileReader` seams + master `Pi5Collector`
  (enable/disable, success/duration, drop-on-fail) + `Factory` flags.
- `internal/cache/cache.go` — ticker + atomic snapshot (the required async-scrape model).
- `docs/mailbox.md` — the provenance record.
- Reference (read-only): `/usr/src/linux-headers-6.12.75+rpt-common-rpi/include/soc/bcm2835/raspberrypi-firmware.h`.

---

## Verification (end-to-end)

1. **Unit suite (hermetic):** `go test -race ./...` — all parsers, model detection, mailbox
   framing, collectors (golden `.prom` + promlint), master collector, cache/ticker (fake clock),
   handler (incl. the never-collect-on-scrape spy). Must pass with no hardware.
2. **Lint/format/build:** `go vet ./...`, `gofmt -l`, cross-compile `GOARCH=arm64 go build`.
3. **Hardware test (on this Pi 5):** `make test-hw` (`go test -tags pi5_hardware ./...`) —
   real `/dev/vcio` round-trip.
4. **Live smoke:** `go run . --collection.interval=2s &` then `curl -s localhost:9110/metrics`
   — confirm `pi5_pmic_rail_watts{rail="VDD_CORE"}`, `pi5_throttle_state{flag="under_voltage"}`,
   `pi5_soc_temperature_celsius`, `pi5_board_info{…}`, and `pi5_exporter_metrics_age_seconds`
   are present and sane. Compare a couple of values against `vcgencmd` directly.
5. **Cache behavior:** scrape `/metrics` repeatedly within one interval → values stable and
   `pi5_exporter_metrics_age_seconds` increasing; confirm via the spy test that scrapes don't
   trigger firmware reads.
6. **Failure modes:** run as a user *not* in `video` → clear permission error naming the group;
   disable a collector via flag → its series absent; kill a collector's data source → `success=0`.
