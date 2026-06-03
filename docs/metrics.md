# pi5_exporter — Metric Reference

This is the complete metric reference for `pi5_exporter`, a Prometheus exporter
for Raspberry Pi 5 (Broadcom BCM2712) firmware/mailbox telemetry that
node_exporter does not cover.

## How metrics are produced

Metrics are collected on an internal ticker (`--collection.interval`, default
`15s`) into an atomically-swapped snapshot; the `/metrics` endpoint serves the
**cached** snapshot and never reads hardware on a scrape. Because of this,
**set the Prometheus `scrape_interval` >= `--collection.interval`** — scraping
faster only re-serves the same cached values.

A collector that **fails on a tick has its data series dropped** (they go absent)
rather than replaying stale values. The failure is signalled instead by the
always-present meta-metrics (`pi5_scrape_collector_success` goes to `0`, and
`pi5_scrape_collector_last_success_timestamp_seconds` stops advancing). This is
the node_exporter pattern.

Firmware (mailbox) collectors require a BCM2712 board and an openable
`/dev/vcio`. If the firmware is unavailable (non-Pi-5 board, or `/dev/vcio` not
openable), those collectors are skipped with a warning at startup; the sysfs
collectors (`rtc`, `watchdog`) still run. `/dev/vcio` is `crw-rw---- root:video`,
so the exporter user must be in the `video` group (`EACCES` → that hint;
`ENOENT` → "not a Pi 5 / firmware too old").

The "Source" column gives the underlying read:

- A **gencmd string** (e.g. `measure_temp`) is sent over the VideoCore mailbox
  property interface via `ioctl(/dev/vcio, 0xC0086400)` using the
  `GET_GENCMD_RESULT` tag (`0x00030080`). These are the firmware collectors.
- A **sysfs / device-tree path** is read directly from the kernel; these run
  regardless of firmware availability.

---

## throttle (default ON, firmware)

Decoded firmware throttle / under-voltage state from `get_throttled`. The four
flags are exposed both as a **live "now"** series (`pi5_throttle_state`) and a
**sticky "since boot"** series (`pi5_throttle_state_since_boot`) using a `flag`
label. The sticky bits latch on the first occurrence and clear **only on a power
cycle, not a reboot** — they have no sysfs equivalent, which is a primary reason
this exporter exists.

`flag` label values: `under_voltage`, `arm_frequency_capped`, `throttled`,
`soft_temp_limit` (get_throttled bits 0–3 live, bits 16–19 sticky).

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_throttle_state` | gauge | `flag` | bool (0/1) | gencmd `get_throttled` | ON | Current firmware throttle/under-voltage condition (1 = active), per flag. |
| `pi5_throttle_state_since_boot` | gauge | `flag` | bool (0/1) | gencmd `get_throttled` | ON | Firmware throttle/under-voltage condition latched at any point since boot (1 = occurred), per flag. Sticky bits clear only on power-cycle. |
| `pi5_throttle_flags` | gauge | (none) | bitfield | gencmd `get_throttled` | ON | Raw get_throttled bitfield value (debug; prefer the per-flag pi5_throttle_state metrics). |

**Labeled-state schema:** there is no `flag="..."` time series with value 1
"selecting" a single active flag; instead, every flag is always present and its
0/1 value tells you whether that condition is active. Use the `_since_boot`
variant to detect transient events (e.g. a brief under-voltage dip) that the
live series may have already cleared between ticks.

---

## pmic (default ON, firmware)

Per-rail voltage, current, and computed power from the PMIC, via
`pmic_read_adc`. The firmware emits a `_A` (current) and a `_V` (voltage) channel
per rail; rails that expose **both** also get a per-rail watts series and
contribute to the measured-power sum. `VDD_CORE` is typically the dominant rail;
live total is ~2.1 W.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_pmic_rail_volts` | gauge | `rail` | volts | gencmd `pmic_read_adc` | ON | PMIC-measured rail voltage in volts. Emitted for every rail with a `_V` channel. |
| `pi5_pmic_rail_amperes` | gauge | `rail` | amperes | gencmd `pmic_read_adc` | ON | PMIC-measured rail current in amperes. Emitted for every rail with an `_A` channel. |
| `pi5_pmic_rail_watts` | gauge | `rail` | watts | gencmd `pmic_read_adc` | ON | Per-rail power (volts*amps), for rails that expose both a voltage and a current channel. |
| `pi5_pmic_measured_power_watts` | gauge | (none) | watts | gencmd `pmic_read_adc` | ON | Sum of per-rail measured power. This is the sum of independently-measured rails, NOT total board input power (there is no 5V-input current channel; EXT5V and BATT are voltage-only). |

**Measured-power caveat (important):** `pi5_pmic_measured_power_watts` is the sum
of the per-rail `volts*amps` for rails that have *both* channels. It is **not**
total board input power — there is no 5V-input current channel to measure it.
Rails that are **voltage-only** (notably `EXT5V` and `BATT`) have no `_A` channel,
so they produce a `pi5_pmic_rail_volts` series but **no** `pi5_pmic_rail_watts`
series and are excluded from the sum.

---

## voltage (default ON, firmware)

Firmware-reported voltages by domain via `measure_volts <domain>`, one gencmd per
domain. Domains are sent verbatim and must be exact — an unknown domain silently
returns the core voltage, so the domain list is fixed to avoid mislabeling.

`domain` label values: `core`, `sdram_c`, `sdram_i`, `sdram_p`.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_voltage_volts` | gauge | `domain` | volts | gencmd `measure_volts <domain>` (`core`, `sdram_c`, `sdram_i`, `sdram_p`) | ON | Firmware-reported voltage by domain (measure_volts). A single domain that fails to parse is skipped with a warning; a transport/firmware failure drops the whole collector. |

---

## clock (default ON, firmware)

Firmware-measured clock frequencies by domain via `measure_clock <domain>`, one
gencmd per domain.

`domain` label values: `arm`, `core`, `v3d`, `isp`, `h264`, `hevc`, `pixel`,
`hdmi`, `emmc`, `uart`, `pwm`, `vec`, `dpi`.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_clock_hertz` | gauge | `domain` | hertz | gencmd `measure_clock <domain>` (13 domains above) | ON | Firmware-measured clock frequency by domain (measure_clock). A value of 0 means the domain is idle / clock-gated, not broken. |

**A value of `0` is valid, not an error.** Several domains (`h264`, `pixel`,
`pwm`, `vec`, `dpi`) commonly read `0` when idle / clock-gated. The domains are
emitted anyway so that **absent vs. zero is unambiguous**: absent means the
collector failed; `0` means the clock is genuinely idle. `arm` overlaps
node_exporter's cpufreq but is the *firmware-measured* rate and is included for
standalone use.

---

## temperature (default ON, firmware)

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_soc_temperature_celsius` | gauge | (none) | celsius | gencmd `measure_temp` | ON | SoC temperature from the firmware sensor (measure_temp). Overlaps node_exporter's thermal_zone0; provided for standalone use. |
| `pi5_pmic_temperature_celsius` | gauge | (none) | celsius | gencmd `measure_temp pmic` | ON | PMIC die temperature (measure_temp pmic). Not exposed by node_exporter. |

**Overlap note:** `pi5_soc_temperature_celsius` is the **one intentional, small
overlap** with node_exporter (which exposes the same reading via
`thermal_zone0`). It is included so the exporter is useful standalone. By
contrast, `pi5_pmic_temperature_celsius` (PMIC die temperature) is **not** in
node_exporter — it is unique to this exporter. The PMIC reading is best-effort:
if `measure_temp pmic` is unavailable it is skipped with a warning rather than
failing the whole collector (the SoC reading still publishes).

---

## rtc (default ON, sysfs)

RTC backup-cell (battery/supercap) voltage and trickle-charge configuration, read
from `/sys/class/rtc/rtc0/*` (microvolt sysfs attributes, converted to volts).
This collector is **not** a firmware collector — it runs even when the mailbox is
unavailable — and is gated on the sysfs path existing. `battery_voltage` is
required (its absence reports `success=0` rather than silently emitting nothing);
the three charging attributes are best-effort.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_rtc_battery_volts` | gauge | (none) | volts | sysfs `/sys/class/rtc/rtc0/battery_voltage` | ON | RTC backup-cell (battery/supercap) voltage. Required; collector fails if missing. |
| `pi5_rtc_charging_volts` | gauge | (none) | volts | sysfs `/sys/class/rtc/rtc0/charging_voltage` | ON | RTC backup-cell trickle-charge target voltage; 0 means trickle charging is not enabled (it does not indicate cell presence). |
| `pi5_rtc_charging_volts_min` | gauge | (none) | volts | sysfs `/sys/class/rtc/rtc0/charging_voltage_min` | ON | Minimum configurable RTC trickle-charge voltage. |
| `pi5_rtc_charging_volts_max` | gauge | (none) | volts | sysfs `/sys/class/rtc/rtc0/charging_voltage_max` | ON | Maximum configurable RTC trickle-charge voltage. |

**`pi5_rtc_charging_volts = 0` meaning:** a value of `0` means the RTC's trickle
charger is **not enabled**. It is the configured charge *target* voltage, not the
measured cell voltage, and it says **nothing about whether a backup cell is
fitted** — leaving charging at `0` is normal and correct for a non-rechargeable
cell. Read `pi5_rtc_battery_volts` for the actual cell voltage.

---

## board (default ON, firmware)

Board identity as a single info-style metric whose value is always `1`; the
information is carried in labels. Model/serial/SoC come from device-tree; the
firmware hash/variant come from `vcgencmd version`.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_board_info` | gauge | `model`, `soc`, `firmware_hash`, `firmware_variant`, `serial` | info (value always 1) | device-tree (`/proc/device-tree/model`, `/proc/device-tree/serial-number`, `/proc/device-tree/compatible`) + gencmd `version` | ON | Board identity as labels; the value is always 1. `model`/`serial`/`soc` from device-tree; `firmware_hash`/`firmware_variant` from `vcgencmd version`. `model` is required (the collector fails if it cannot be read); the other label fields are left empty if unavailable. |

> Note: this collector is registered as a firmware collector, so it is skipped
> when the mailbox is unavailable — even though the device-tree fields could be
> read without it.

---

## watchdog (default OFF, sysfs)

Hardware watchdog state from `/sys/class/watchdog/watchdog0/*`. Enable with
`--collector.watchdog`. Not a firmware collector. `bootstatus` is required; the
`timeout` attribute is best-effort.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_watchdog_bootstatus` | gauge | (none) | bitfield | sysfs `/sys/class/watchdog/watchdog0/bootstatus` | OFF | Hardware watchdog boot status (0 = clean; non-zero = last boot was watchdog-triggered). |
| `pi5_watchdog_timeout_seconds` | gauge | (none) | seconds | sysfs `/sys/class/watchdog/watchdog0/timeout` | OFF | Configured hardware watchdog timeout. |

---

## ringosc (default OFF, firmware)

Ring oscillator frequency, a diagnostic. Enable with `--collector.ringosc`.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_ring_osc_hertz` | gauge | (none) | hertz | gencmd `read_ring_osc` | OFF | Ring oscillator frequency from read_ring_osc (diagnostic). Firmware reports MHz; scaled to Hz. |

---

## reset (default OFF, firmware)

Raw firmware reset-status bitfield, a diagnostic. Enable with
`--collector.reset`.

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_reset_status` | gauge | (none) | bitfield | gencmd `get_rsts` | OFF | Raw firmware reset status from get_rsts (diagnostic bitfield). |

---

## Meta / exporter metrics (always present)

These describe the exporter itself and the freshness/health of the cached
snapshot. They are **always present** regardless of which data collectors
succeeded — that is how a failed collector is detected (its data series go
absent, but its `pi5_scrape_collector_success` flips to 0).

### Per-collector scrape meta-metrics

Emitted by the master collector for every enabled collector, labelled by
`collector` (e.g. `throttle`, `pmic`, `clock`, ...).

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_scrape_collector_success` | gauge | `collector` | bool (0/1) | internal | always | 1 if the named collector succeeded on the last collection cycle, 0 otherwise. |
| `pi5_scrape_collector_duration_seconds` | gauge | `collector` | seconds | internal | always | Duration of the named collector's last collection cycle. Emitted even when the collector fails. |
| `pi5_scrape_collector_last_success_timestamp_seconds` | gauge | `collector` | unix seconds | internal | always | Unix time of the named collector's last successful collection cycle. Stops advancing while a collector is failing (absent until the first success). |

### Snapshot freshness / collection-cycle meta-metrics

The `metrics_age` value is computed **at scrape time**, so it stays truthful
between ticks (it grows as the cached snapshot ages, then resets on the next
collection).

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_exporter_metrics_age_seconds` | gauge | (none) | seconds | internal (cache) | always | Seconds since the currently-served metrics were collected. Computed at scrape time, not tick time. |
| `pi5_exporter_last_collection_timestamp_seconds` | gauge | (none) | unix seconds | internal (cache) | always | Unix time at which the currently-served metrics were collected. |
| `pi5_exporter_last_collection_duration_seconds` | gauge | (none) | seconds | internal (cache) | always | Duration of the collection cycle that produced the currently-served metrics. |

### Build info

Emitted by the standard prometheus/client_golang version collector
(`versioncollector.NewCollector("pi5_exporter")`).

| Metric | Type | Labels | Unit | Source | Default | Notes |
|---|---|---|---|---|---|---|
| `pi5_exporter_build_info` | gauge | `version`, `revision`, `branch`, `goversion`, `goos`, `goarch`, `tags` | info (value always 1) | build-time ldflags | always | A metric with a constant '1' value labeled by version, revision, branch, goversion from which pi5_exporter was built, and the goos and goarch for the build. |

---

## Relationship to node_exporter

`pi5_exporter` is meant to run **alongside** node_exporter, not replace it. It
deliberately exposes only Pi-5-specific firmware/mailbox telemetry that
node_exporter does not, and intentionally does **not** export the following,
because node_exporter already covers them:

- **Generic hwmon** — e.g. the `rpi_volt` under-voltage alarm, the PWM fan
  (`pwmfan`), NVMe sensors, and the RP1 ADC (`rp1_adc`). node_exporter's hwmon
  collector handles these.
- **`thermal_zone` plumbing** — node_exporter's thermal-zone collector already
  exposes the kernel thermal zones.
- **cpufreq** — CPU frequency scaling is covered by node_exporter's cpufreq
  collector.
- **CPU / memory / disk / network** — core host metrics are node_exporter's job.

**The one intentional overlap** is SoC temperature
(`pi5_soc_temperature_celsius`), which duplicates node_exporter's
`thermal_zone0`. It is included so this exporter is useful on its own; if you
already run node_exporter you can ignore it. Everything else here is
non-overlapping — notably the **sticky since-boot throttle bits**, **per-rail
PMIC power**, **firmware-measured voltages/clocks**, **PMIC die temperature**,
and the **RTC backup-cell voltage** — none of which node_exporter provides.
