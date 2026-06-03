# pi5_exporter

A [Prometheus](https://prometheus.io/docs/introduction/overview/) [exporter](https://prometheus.io/docs/instrumenting/exporters/)
that runs **on a Raspberry Pi 5** and exposes firmware / mailbox telemetry that
`node_exporter` cannot reach: PMIC per-rail power, decoded throttle and
under-voltage state (including the sticky "since boot" bits), firmware-measured
voltages and clocks, SoC/PMIC temperature, and the RTC backup-cell voltage. It
talks to the VideoCore firmware over the BCM2712 mailbox property interface
(`/dev/vcio`) — the same path `vcgencmd` uses — and serves the result on
`:2712`.

> **This is NOT another node_exporter.** It does not duplicate CPU, memory,
> disk, network, filesystem, hwmon, or cpufreq metrics. It **complements**
> `node_exporter` by surfacing data that only the Pi's firmware knows:
> per-rail PMIC voltage/current/power, the *sticky* under-voltage and throttle
> flags that persist until a power-cycle, firmware voltage and clock domains, and
> the real-time-clock backup cell. Run both side by side. The only intentional
> overlap is SoC temperature, included so the exporter is also useful standalone.

---

## 1. Requirements

| Requirement | Notes |
|---|---|
| **Raspberry Pi 5 / BCM2712** | The Pi 500 and Compute Module 5 are also BCM2712 and work. The firmware collectors are gated on a BCM2712 board. |
| **`/dev/vcio`** | The firmware mailbox character device. Present on a current Raspberry Pi OS. |
| The exporter user in the **`video` group** | See the callout below — this is the #1 gotcha. |
| Go 1.26+ (build only) | Only needed to build; the resulting binary is static (`CGO_ENABLED=0`). |

A Raspberry Pi 4 (and earlier) is **not** supported for the firmware metrics:
it lacks the `pmic_read_adc` firmware command and the BCM2712 PMIC. On a non-Pi-5
board the firmware collectors auto-disable (see Troubleshooting); only the sysfs
collectors (`rtc`, `watchdog`) can run.

> **The exporter user MUST be in the `video` group.**
>
> `/dev/vcio` is `crw-rw---- root:video`. The exporter reaches the VideoCore
> firmware (PMIC, throttle, clocks, voltages, temperature) by `ioctl()` on that
> device, so the process needs group `video` to open it.
>
> ```console
> $ ls -l /dev/vcio
> crw-rw---- 1 root video 249, 0 Jun  3 03:00 /dev/vcio
> $ sudo usermod -aG video <exporter-user>   # then restart the service
> ```
>
> If the process cannot open `/dev/vcio` it logs a warning and **silently skips
> all firmware collectors** rather than crashing — so a quietly empty set of
> `pi5_*` firmware metrics almost always means a missing `video` group
> (`EACCES`). A missing device (`ENOENT`) means "not a Pi 5 / firmware too old".

---

## 2. Install & build

### Build from source

```sh
make build          # host arch  -> ./pi5_exporter
make build-arm64    # explicit arm64 build (the Pi 5 target)
```

`make build` injects version metadata via `-ldflags` into
`github.com/prometheus/common/version`, so the binary reports a sensible
`--version` and exposes `pi5_exporter_build_info{version,revision,branch,goversion,...}`.
`VERSION`/`REVISION`/`BRANCH` are derived from `git` by default and can be
overridden on the command line (e.g. `make build VERSION=1.0.0`).

### Or `go install`

```sh
go install github.com/bcrisp4/pi5_exporter@latest
```

(`go install` does not set the version ldflags, so `pi5_exporter_build_info`
will report the module build info rather than a tagged version — use `make build`
for release artifacts.)

### Dedicated user + systemd

A hardened unit ships at [`systemd/pi5_exporter.service`](systemd/pi5_exporter.service).
Create a locked-down system user that is a member of `video`, install the binary
and the unit, then enable it:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin --groups video pi5_exporter
sudo install -m 0755 pi5_exporter /usr/local/bin/pi5_exporter
sudo install -m 0644 systemd/pi5_exporter.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now pi5_exporter
```

The unit runs as the `pi5_exporter` user with `SupplementaryGroups=video` (so it
can open `/dev/vcio`) and applies systemd hardening (`NoNewPrivileges`,
`ProtectSystem=strict`, `MemoryDenyWriteExecute`, an empty capability set,
`DeviceAllow=/dev/vcio rw`, `DeviceAllow=char-rtc r`, `MemoryMax=64M`, etc.).

---

## 3. Run

```sh
./pi5_exporter                                   # listens on :2712
./pi5_exporter --collection.interval=30s
./pi5_exporter --collector.watchdog --no-collector.rtc
```

Then scrape `http://<pi>:2712/metrics`. A landing page is served at `/`.

### Flags

All flags are kingpin-style (`--flag` / `--flag=value`).

| Flag | Default | Meaning |
|---|---|---|
| `--collector.<name>` | per-collector (see table below) | Enable the named collector. |
| `--no-collector.<name>` | — | Disable the named collector (negated form of the above). |
| `--collection.interval` | `15s` | How often the internal ticker reads the hardware. `/metrics` serves the latest cached values, so set Prometheus `scrape_interval` ≥ this. |
| `--web.listen-address` | `:2712` | Address/port to listen on. `2712` is the BCM2712 mnemonic (the 9100–9999 exporter range is already fully allocated). May be repeated for multiple addresses. |
| `--web.config.file` | _(none)_ | [exporter-toolkit](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md) web config for TLS and HTTP basic auth. |
| `--log.level` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `--log.format` | `logfmt` | Log format: `logfmt` or `json`. |
| `--version` | — | Print version and exit. |
| `-h`, `--help` | — | Show help and exit. |

### Collector enable/disable model

Every collector has a `--collector.<name>` flag (and its negation
`--no-collector.<name>`). The defaults below match the registry in
`internal/collector/factory.go`:

| Collector | Default | Transport | Metrics |
|---|---|---|---|
| `throttle` | **on** | firmware | `pi5_throttle_state`, `pi5_throttle_state_since_boot`, `pi5_throttle_flags` |
| `pmic` | **on** | firmware | `pi5_pmic_rail_volts`, `pi5_pmic_rail_amperes`, `pi5_pmic_rail_watts`, `pi5_pmic_measured_power_watts` |
| `voltage` | **on** | firmware | `pi5_voltage_volts` |
| `clock` | **on** | firmware | `pi5_clock_hertz` |
| `temperature` | **on** | firmware | `pi5_soc_temperature_celsius`, `pi5_pmic_temperature_celsius` |
| `board` | **on** | firmware | `pi5_board_info` |
| `rtc` | **on** | sysfs | `pi5_rtc_battery_volts`, `pi5_rtc_charging_volts`, `pi5_rtc_charging_volts_min`, `pi5_rtc_charging_volts_max` |
| `watchdog` | off | sysfs | `pi5_watchdog_bootstatus`, `pi5_watchdog_timeout_seconds` |
| `ringosc` | off | firmware | `pi5_ring_osc_hertz` |
| `reset` | off | firmware | `pi5_reset_status` |

**Firmware** collectors need `/dev/vcio`; they are skipped (with a warning) on a
non-Pi-5 board or when the device can't be opened. **sysfs** collectors (`rtc`,
`watchdog`) run regardless and are gated only on their sysfs path existing.

---

## 4. How it works / caching

```
                 every --collection.interval (default 15s)
   ┌──────────┐      ┌───────────────┐      ┌───────────────┐
   │  ticker  │────▶│   collectors   │────▶│ atomic snapshot │
   └──────────┘      │ (read /dev/vcio │      │  []MetricFamily │
                     │  + sysfs once)  │      └───────┬───────┘
                     └───────────────┘              │ served as-is
                                                     ▼
                         GET /metrics  ◀────  cached snapshot (no HW read)
```

- An **internal ticker** collects *all* enabled collectors every
  `--collection.interval` into an atomically-swapped snapshot.
- **`/metrics` serves that cached snapshot** — the hardware is **never** read on
  a scrape. An **eager first collection** runs before the server starts, so the
  very first scrape is already populated.
- **Therefore set Prometheus `scrape_interval` ≥ `--collection.interval`.**
  Scraping faster than you collect just re-serves identical values.
- **Watch `pi5_exporter_metrics_age_seconds`** (computed fresh at scrape time) to
  detect a stuck collector loop. Also available:
  `pi5_exporter_last_collection_timestamp_seconds`,
  `pi5_exporter_last_collection_duration_seconds`.
- **Drop-on-fail:** if a collector errors on a tick, its data series are
  **dropped** (they go *absent*) rather than replaying stale values. The failure
  is signalled by the always-present meta-metric
  `pi5_scrape_collector_success{collector="…"} == 0`. Alert on that, and on the
  `…_last_success_timestamp_seconds` going stale — not on the data series, which
  legitimately disappear.

---

## 5. Prometheus scrape config

```yaml
# prometheus.yml
scrape_configs:
  - job_name: pi5_exporter
    # MUST be >= the exporter's --collection.interval (default 15s),
    # because /metrics serves cached values.
    scrape_interval: 30s
    static_configs:
      - targets: ['raspberrypi:2712']
```

---

## 6. Example PromQL & alerting rules

Ad-hoc queries:

```promql
# Live under-voltage happening right now
pi5_throttle_state{flag="under_voltage"} == 1

# Under-voltage occurred at some point since the last power-cycle (sticky)
pi5_throttle_state_since_boot{flag="under_voltage"} == 1

# Total measured board power (sum of measured rails; not 5V input power)
pi5_pmic_measured_power_watts

# The 5 hungriest rails right now
topk(5, pi5_pmic_rail_watts)

# SoC temperature
pi5_soc_temperature_celsius
```

Alerting rules:

```yaml
groups:
  - name: pi5_exporter
    rules:
      # Power supply is sagging RIGHT NOW — risk of corruption/instability.
      - alert: Pi5UnderVoltageLive
        expr: pi5_throttle_state{flag="under_voltage"} == 1
        for: 1m
        labels: { severity: critical }
        annotations:
          summary: "Pi 5 {{ $labels.instance }} is under-voltage now"
          description: "Check the PSU/cable; the firmware is reporting active under-voltage."

      # Under-voltage happened at some point since boot (sticky bit; clears only
      # on power-cycle, never on reboot — no sysfs equivalent exists).
      - alert: Pi5UnderVoltageSinceBoot
        expr: pi5_throttle_state_since_boot{flag="under_voltage"} == 1
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Pi 5 {{ $labels.instance }} saw under-voltage since power-on"
          description: "A power dip occurred earlier; investigate even if currently stable."

      - alert: Pi5HighSocTemperature
        expr: pi5_soc_temperature_celsius > 80
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Pi 5 {{ $labels.instance }} SoC at {{ $value | printf \"%.1f\" }}C"

      # RTC backup cell is low (0 = charging disabled / no cell fitted, so guard > 0).
      - alert: Pi5RtcCellLow
        expr: pi5_rtc_battery_volts > 0 and pi5_rtc_battery_volts < 2.8
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "Pi 5 {{ $labels.instance }} RTC backup cell low ({{ $value | printf \"%.2f\" }}V)"

      # A collector is failing — its data series have gone absent.
      - alert: Pi5CollectorFailed
        expr: pi5_scrape_collector_success == 0
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "pi5_exporter collector {{ $labels.collector }} failing on {{ $labels.instance }}"

      # Cached metrics are stale — the internal collection loop is stuck.
      - alert: Pi5MetricsStale
        expr: pi5_exporter_metrics_age_seconds > 120
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "pi5_exporter metrics on {{ $labels.instance }} are {{ $value | printf \"%.0f\" }}s old"
```

---

## 7. Metric reference

Full details — including labels, units, and provenance — are in
[`docs/metrics.md`](docs/metrics.md). Summary of the data metrics:

| Metric | Type | Labels | Description |
|---|---|---|---|
| `pi5_throttle_state` | gauge 0/1 | `flag` | Live throttle/under-voltage condition. |
| `pi5_throttle_state_since_boot` | gauge 0/1 | `flag` | Sticky condition latched since power-on. |
| `pi5_throttle_flags` | gauge | — | Raw `get_throttled` bitfield (debug). |
| `pi5_pmic_rail_volts` | gauge | `rail` | PMIC-measured rail voltage (V). |
| `pi5_pmic_rail_amperes` | gauge | `rail` | PMIC-measured rail current (A). |
| `pi5_pmic_rail_watts` | gauge | `rail` | Per-rail power (V·A), rails with both channels. |
| `pi5_pmic_measured_power_watts` | gauge | — | Sum of measured rails (not total board input power). |
| `pi5_voltage_volts` | gauge | `domain` | Firmware voltage (`core`, `sdram_c`, `sdram_i`, `sdram_p`). |
| `pi5_clock_hertz` | gauge | `domain` | Firmware clock (0 = idle/clock-gated). |
| `pi5_soc_temperature_celsius` | gauge | — | SoC temperature (overlaps node_exporter). |
| `pi5_pmic_temperature_celsius` | gauge | — | PMIC die temperature (not in node_exporter). |
| `pi5_rtc_battery_volts` | gauge | — | RTC backup-cell voltage. |
| `pi5_rtc_charging_volts` | gauge | — | RTC trickle-charge target (0 = disabled/no cell). |
| `pi5_rtc_charging_volts_min` / `_max` | gauge | — | Configurable trickle-charge bounds. |
| `pi5_board_info` | gauge (=1) | `model`,`soc`,`firmware_hash`,`firmware_variant`,`serial` | Board identity. |
| `pi5_watchdog_bootstatus` / `pi5_watchdog_timeout_seconds` | gauge | — | Watchdog (collector off by default). |
| `pi5_ring_osc_hertz` | gauge | — | Ring oscillator (collector off by default). |
| `pi5_reset_status` | gauge | — | Raw `get_rsts` (collector off by default). |

Throttle/sticky `flag` values: `under_voltage`, `arm_frequency_capped`,
`throttled`, `soft_temp_limit`. The `0–3` bits are live "now"; bits `16–19` are
the sticky "since boot" values that clear only on a power-cycle.

Always-present meta-metrics:

| Metric | Labels | Description |
|---|---|---|
| `pi5_exporter_build_info` | `version`,`revision`,`branch`,`goversion`,… | Build metadata (=1). |
| `pi5_scrape_collector_success` | `collector` | 1 if the collector succeeded last cycle, else 0. |
| `pi5_scrape_collector_duration_seconds` | `collector` | Last cycle duration. |
| `pi5_scrape_collector_last_success_timestamp_seconds` | `collector` | Unix time of last success. |
| `pi5_exporter_metrics_age_seconds` | — | Age of the served snapshot (computed at scrape time). |
| `pi5_exporter_last_collection_timestamp_seconds` | — | When the served snapshot was collected. |
| `pi5_exporter_last_collection_duration_seconds` | — | Duration of that collection cycle. |

---

## 8. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| All `pi5_*` firmware metrics absent; log warns `cannot open /dev/vcio` with `permission denied` (`EACCES`) | The exporter user is not in the `video` group. | `sudo usermod -aG video <user>` and restart the service. The systemd unit already sets `SupplementaryGroups=video`. |
| Firmware collectors disabled; log warns `not a BCM2712 (Raspberry Pi 5) board` | Running on a Pi 4 or non-Pi hardware. | Expected — firmware collectors only work on BCM2712. sysfs collectors still run. |
| Log warns `cannot open /dev/vcio` with `no such file` (`ENOENT`) | Not a Pi 5, or firmware too old to expose `/dev/vcio`. | Update the firmware / confirm the board. |
| `pi5_rtc_*` metrics absent | No `/sys/class/rtc/rtc0` (or `pi5_scrape_collector_success{collector="rtc"}==0`). | The RTC collector is gated on that path; `pi5_rtc_charging_volts == 0` means trickle charging is disabled / no cell is fitted (not an error). |
| Metrics look frozen / `pi5_exporter_metrics_age_seconds` keeps climbing | Scrape interval shorter than collection interval, or a stuck collection loop. | Set `scrape_interval ≥ --collection.interval`; inspect logs. |

---

## Documentation

- [`docs/mailbox.md`](docs/mailbox.md) — the `/dev/vcio` mailbox property
  interface, gencmd transport, and the error contract.
- [`docs/metrics.md`](docs/metrics.md) — full metric reference with units and
  provenance.
- [`docs/design.md`](docs/design.md) — architecture: collect-on-interval /
  serve-from-cache, drop-on-fail, and the node_exporter non-overlap rationale.

## License

[Apache-2.0](LICENSE).
