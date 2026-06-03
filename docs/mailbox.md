# The `/dev/vcio` mailbox transport

This is the provenance / audit document for `internal/mailbox` — the single
load-bearing, most non-obvious piece of code in `pi5_exporter`. It explains how
the exporter talks to the Raspberry Pi 5 VideoCore firmware, why the magic
numbers are what they are, and how the code in
`internal/mailbox/{mailbox.go,client.go,open.go}` maps onto the on-the-wire
protocol. Every claim here is either traceable to a cited source or was verified
live on this exact board (BCM2712) this session; see [Sources](#9-sources).

---

## 1. Background: why the mailbox at all

On a Raspberry Pi the **VideoCore firmware** (not the ARM/Linux side) owns the
clocks, voltage rails, the PMIC, and the throttle/under-voltage state machine.
Linux reaches the firmware over the **mailbox property interface**, a shared
buffer the ARM hands to the firmware via an `ioctl` on a character device.

On earlier Pis, `vcgencmd` reached the firmware "gencmd" channel through
`/dev/vchiq`. **The Pi 5 has no `/dev/vchiq`.** On the Pi 5, `vcgencmd` routes
*everything* — including gencmd strings such as `measure_temp` — through the
mailbox property interface exposed by `/dev/vcio`, via a single `ioctl`.

This is not inferred from documentation; it was observed directly on this board:

- **strace evidence.** Running `vcgencmd measure_temp` under `strace` shows it
  `openat(... "/dev/vcio" ...)` and then issues a single
  `ioctl(fd, 0xc0086400, ...)` carrying the property buffer — there is no
  `/dev/vchiq` access at all.
- **Pure-Go hardware test.** `internal/mailbox/mailbox_hw_test.go`
  (`//go:build pi5_hardware`, `TestGenCmd_RealHardware`) opens `/dev/vcio`,
  builds the request in Go, performs the raw `ioctl`, and parses the reply. It
  returned a sane `temp=...'C` reading (≈48.3 °C this session), proving the Go
  reimplementation reproduces `vcgencmd` byte-for-byte without linking any C.

`internal/mailbox` therefore reimplements exactly the small slice of the mailbox
property protocol needed to issue gencmd strings and read back their ASCII
results. Everything in the package is pure and unit-testable except a single
`ioctl` syscall, which is injected behind an `IoctlFunc` seam (`client.go`) so
the marshalling can be exercised on non-Pi CI hosts.

---

## 2. The ioctl request number: `0xC0086400`

`open.go` defines:

```go
const ioctlGenCmd = 0xC0086400 // _IOWR('d'=100, 0, char*) on arm64
```

This is `_IOWR(VCIO_IOC_MAGIC, 0, char *)`, where `VCIO_IOC_MAGIC` is `'d'`
(= 100) per `raspberrypi/utils` `vcgencmd.c`. The Linux `_IOC` encoding packs
four bitfields:

```
ioctl = (dir << 30) | (size << 16) | (type << 8) | (nr)
```

On arm64 (LP64, 64-bit kernel):

| Field  | Meaning                       | Value            | Bits           |
|--------|-------------------------------|------------------|----------------|
| `dir`  | `_IOC_READ | _IOC_WRITE` (RW) | `3`              | `[31:30]`      |
| `size` | `sizeof(char *)` = 8 bytes    | `8` = `0x08`     | `[29:16]`      |
| `type` | magic `'d'`                   | `100` = `0x64`   | `[15:8]`       |
| `nr`   | command number                | `0`              | `[7:0]`        |

Derivation:

```
(3 << 30) | (8 << 16) | (100 << 8) | 0
= 0xC0000000 | 0x00080000 | 0x00006400 | 0x0
= 0xC0086400
```

The `size` field is `8` specifically because it is `sizeof(char *)` on a 64-bit
kernel; the third `ioctl` argument is a *pointer* to the property buffer, not the
buffer itself. This value was confirmed live: the same `0xc0086400` appears in
the `strace` of `vcgencmd`, and recomputation matches.

`vcioIoctl` (`open.go`) issues it with `unix.Syscall(SYS_IOCTL, fd,
ioctlGenCmd, unsafe.Pointer(&buf[0]))`. The firmware reads and writes that buffer
**in place**; this is the only impure operation in the package.

---

## 3. The gencmd tag: `0x00030080` (`GET_GENCMD_RESULT`)

```go
const tagGetGenCmdResult = 0x00030080 // mailbox.go
const maxStringBytes     = 4096        // MAX_STRING
```

`GET_GENCMD_RESULT = 0x00030080` is the mailbox property *tag* that selects the
gencmd channel: "run this command string and give me back its textual result."
`MAX_STRING = 4096` is the size in bytes of the value buffer that holds the
command on the way in and the result on the way out.

**This tag is deliberately absent from the mainline kernel mailbox header**
(`raspberrypi-firmware.h`, see §8). That is expected: the *kernel never issues
gencmd* — it only ever uses the binary property tags. The gencmd tag lives in
userspace, in `raspberrypi/utils` `vcgencmd.c`, which is where `vcgencmd` itself
gets it. We took the tag, the magic, and `MAX_STRING` from that source, and
confirmed the tag word in the `strace` buffer dump.

---

## 4. Request / response buffer layout

`BuildGenCmd(cmd)` (`mailbox.go`) allocates a fresh `[]uint32` of
`totalWords = 1031` words (little-endian on arm64) and fills it as follows. The
**same buffer** is mutated in place by the firmware to become the response.

Constants (`mailbox.go`):
`valueWordOffset = 6`, `valueWords = maxStringBytes/4 = 1024`,
`totalWords = 6 + 1024 + 1 = 1031`, total bytes = `1031 * 4 = 4124`.

| Word index | Bytes (LE) | Field                         | Request value             | Response value                          |
|-----------:|-----------:|-------------------------------|---------------------------|-----------------------------------------|
| `[0]`      | 0–3        | Total buffer size in **bytes**| `4124` (`1031*4`)         | (echoed)                                |
| `[1]`      | 4–7        | Request/response code         | `0` (process request)     | firmware sets response code             |
| `[2]`      | 8–11       | **Tag id**                    | `0x00030080`              | (echoed)                                |
| `[3]`      | 12–15      | Value buffer size in **bytes**| `4096` (`MAX_STRING`)     | (echoed)                                |
| `[4]`      | 16–19      | Tag request/response code     | `0`                       | firmware sets tag response code         |
| `[5]`      | 20–23      | **Firmware return code slot** | `0`                       | `0` = ok, non-zero = error (see §5)     |
| `[6 .. 1029]` | 24–4119 | **Value region** (1024 words = 4096 bytes) | command string, NUL-terminated, rest zero | ASCII result, NUL-terminated |
| `[1030]`   | 4120–4123  | Mailbox **end tag**           | `0`                       | `0`                                     |

Notes on the implementation:

- `buf[0]` (total size) is filled in **last**, mirroring the conventional
  assembly order of a mailbox property request.
- `packString(buf, 6, cmd)` writes the command bytes little-endian into the value
  region. Because `make([]uint32, …)` zeroes the buffer, the NUL terminator, any
  trailing slack, and the end tag are already `0` — `packString` only ORs in the
  non-zero string bytes.
- On the way back, `unpackString(buf, 6)` reconstructs the value-region bytes
  little-endian and cuts at the first NUL. The scan is **bounded to the 1024
  value words** so it never reads the end tag.

---

## 5. The error contract

`ParseGenCmdResult(buf)` (`mailbox.go`) reads the body first, then checks
`buf[5]`:

```go
body := unpackString(buf, valueWordOffset)
if buf[5] != 0 {
    return "", &GenCmdError{Code: buf[5], Body: body}
}
return body, nil
```

The key, **verified-live** subtlety is that `buf[5]` only distinguishes "the
firmware recognised the command" from "it did not" — it does **not** flag a
command that ran but rejected its argument. There are four observed cases:

| Case | `ioctl` | `buf[5]` (return code) | Value region (body) | Code's handling |
|------|---------|------------------------|---------------------|-----------------|
| 1. Success | ok | `0` | valid result, e.g. `temp=48.3'C` | `GenCmd` returns the body string |
| 2. Valid-but-zero result | ok | `0` | a legitimate `0`-ish value, e.g. `frequency(0)=0` for an idle block | returned as body; the pure parser accepts it (0 Hz is valid) |
| 3. Command-level error ("bad argument") | ok | `0` | the **error text in the body**, e.g. `bad argument` / `error_msg="…"` | `GenCmd` returns the body with **no error**; the strict pure parser then **rejects** the non-matching body |
| 4. Unknown command | ok | `0xffffffff` | firmware diagnostic text | `ParseGenCmdResult` returns `*GenCmdError{Code:0xffffffff, Body:…}` |

Because case 3 returns `buf[5] == 0`, **the firmware return code cannot be used
to detect a bad argument.** This is why the parsers in `internal/parse` are
*strict and anchored* (e.g. `^volt=([0-9.]+)V$`, `^temp=([0-9.]+)'C$`): a
"bad argument" body simply fails to match and surfaces as a parse error, with **no
string-sniffing** for the word "error". The transport stays dumb; correctness
lives in the anchored regexes.

`Client.GenCmd` (`client.go`) wraps a *transport* failure (a non-zero `errno`
from the `ioctl` itself) with the command name for context; a non-zero firmware
return code surfaces as the `*GenCmdError` from `ParseGenCmdResult`:

```go
func (e *GenCmdError) Error() string {
    return fmt.Sprintf("gencmd firmware error 0x%x: %q", e.Code, e.Body)
}
```

### Open-time errno classification (`open.go`)

`Open()` classifies failures opening `/dev/vcio` into actionable messages:

| `errno`            | Meaning                                              | Message |
|--------------------|------------------------------------------------------|---------|
| `EACCES` / `ErrPermission` | `/dev/vcio` is `crw-rw---- root:video`; caller lacks access | `permission denied (is this user in the 'video' group?)` |
| `ENOENT` / `ErrNotExist`   | device node absent                                  | `no /dev/vcio (not a Pi 5 / firmware too old)` |
| anything else      | unexpected                                           | wrapped raw error |

The `'video'` group requirement is structural: the device node is owned
`root:video` mode `0660`, so the exporter's service user **must** have `video` as
a supplementary group (the systemd unit sets `SupplementaryGroups=video`).

---

## 6. The gencmd command strings this exporter sends

Every firmware read in the exporter goes through `Client.GenCmd(<string>)`. The
complete set of command strings actually sent (grep the collectors), with a
representative single output line each:

| gencmd string         | Collector(s)         | Example firmware output                                  |
|-----------------------|----------------------|----------------------------------------------------------|
| `get_throttled`       | throttle             | `throttled=0x0`                                          |
| `pmic_read_adc`       | pmic                 | `VDD_CORE_A current(7)=0.56259000A` (one line per channel; `_A`/`_V` lines merged) |
| `measure_volts <dom>` | voltage              | `volt=0.8749V`  (`<dom>` ∈ `core,sdram_c,sdram_i,sdram_p`) |
| `measure_clock <dom>` | clock                | `frequency(0)=1600020224`  (Hz; `0` = idle/clock-gated)  |
| `measure_temp`        | temperature, hw test | `temp=48.3'C`  (apostrophe is the firmware's degree glyph)|
| `measure_temp pmic`   | temperature          | `temp=51.0'C`  (PMIC die; best-effort, skipped on failure)|
| `version`             | board                | `version 66f33f7e (release) (embedded)` (last line of a multi-line block) |
| `read_ring_osc`       | ringosc (off)        | `read_ring_osc(2)=9.368MHz (@0.8749V) (46.6'C)`          |
| `get_rsts`            | reset (off)          | `get_rsts=1020`                                          |

Implementation details worth recording:

- **`measure_volts`/`measure_clock` arguments must be exact.** An *unknown*
  voltage domain silently returns the **core** voltage (not an error), so a typo
  would mislabel data. The domain lists are hard-coded constants in
  `voltage.go` / `clock.go`.
- **`pmic_read_adc`** emits two non-adjacent lines per rail (`<rail>_A current…`
  and `<rail>_V volt…`); `parse.ParsePMIC` strips the `_A`/`_V` suffix and merges
  them. Volt-only rails (`EXT5V`, `BATT`) have no current channel, so they never
  get a `_watts` series and are excluded from `pi5_pmic_measured_power_watts`.
- **`measure_temp pmic`** is best-effort: the temperature collector emits the SoC
  temp unconditionally but only *adds* the PMIC die temp if that second call and
  its parse succeed, logging a warning otherwise.
- **`version`** is parsed for `firmware_hash` and `firmware_variant`; only the
  last `version <hash> (<variant>) (<build>)` line is authoritative.

---

## 7. Throttle bit table (`get_throttled`)

`parse.ParseThrottled` decodes the 32-bit mask from `throttled=0x…`. The low
nibble is the **live "now"** state; bits 16–19 are **sticky "since boot"** flags
that latch on first occurrence:

| Bit (now) | Bit (sticky) | `flag` label            | Meaning                          |
|----------:|-------------:|-------------------------|----------------------------------|
| `0`       | `16`         | `under_voltage`         | under-voltage detected           |
| `1`       | `17`         | `arm_frequency_capped`  | ARM frequency capped             |
| `2`       | `18`         | `throttled`             | currently throttled              |
| `3`       | `19`         | `soft_temp_limit`       | soft temperature limit active    |

The "now" bits feed `pi5_throttle_state{flag}`; the sticky bits feed
`pi5_throttle_state_since_boot{flag}`; the raw mask is exposed as
`pi5_throttle_flags` for debugging.

**Why the sticky bits matter:** they clear only on a *power-cycle*, not on a
warm reboot, and have **no sysfs equivalent**. Surfacing them is a primary reason
this exporter exists — a brief under-voltage dip during boot or load latches a
sticky bit that node_exporter cannot see.

---

## 8. Binary property tags (reference, not implemented)

For completeness, the **binary** mailbox property tags the *kernel* uses live in
the mainline header
`/usr/src/linux-headers-6.12.75+rpt-common-rpi/include/soc/bcm2835/raspberrypi-firmware.h`.
The exporter does **not** use any of these; they are documented here purely for
provenance:

| Tag                  | Value        |
|----------------------|--------------|
| `GET_CLOCK_RATE`     | `0x00030002` |
| `GET_VOLTAGE`        | `0x00030003` |
| `GET_TEMPERATURE`    | `0x00030006` |
| `GET_THROTTLED`      | `0x00030046` |
| `STATUS_SUCCESS`     | `0x80000000` |
| `STATUS_ERROR`       | `0x80000001` |

Note that the gencmd tag `GET_GENCMD_RESULT = 0x00030080` we *do* use is **not**
in this header (see §3) — it is a userspace tag from `vcgencmd.c`.

**Why gencmd strings instead of binary tags.** The exporter deliberately uses the
single `GET_GENCMD_RESULT` string channel for everything, rather than a mix of
binary tags. Rationale:

1. **One code path.** A single request/response shape (§4) and a single error
   contract (§5) cover every reading. No per-tag struct marshalling.
2. **PMIC has no binary equivalent.** `pmic_read_adc` is a gencmd-only facility;
   there is no clean binary property tag that enumerates every PMIC rail. Using
   gencmd for *everything* keeps PMIC and the rest uniform.
3. **It is exactly what `vcgencmd` does.** Matching `vcgencmd`'s own transport
   (verified by `strace`) means the firmware sees the identical requests, so our
   results match the canonical tool byte-for-byte.

The cost — strict string parsing — is paid once in `internal/parse` and is
well-contained by the anchored regexes described in §5.

---

## 9. Sources

- **`raspberrypi/utils`, `vcgencmd/vcgencmd.c`** — origin of
  `VCIO_IOC_MAGIC = 'd'` (100), the `_IOWR('d',0,char*)` ioctl, the gencmd tag
  `GET_GENCMD_RESULT = 0x00030080`, and `MAX_STRING = 4096`.
- **Mainline kernel mailbox header** —
  `/usr/src/linux-headers-6.12.75+rpt-common-rpi/include/soc/bcm2835/raspberrypi-firmware.h`,
  source of the binary property tags in §8 (and the fact that the gencmd tag is
  absent from it).
- **raspberrypi.com documentation** — `vcgencmd` command reference,
  `get_throttled` bit definitions, and the revision-codes / board-identity docs.
- **Verified live on this board** (BCM2712 / Pi 5), June 2026, via **`strace`** of
  `vcgencmd` (showing `openat /dev/vcio` + `ioctl 0xc0086400`, no `/dev/vchiq`)
  **and** the pure-Go hardware integration test
  `internal/mailbox/mailbox_hw_test.go` (`TestGenCmd_RealHardware`,
  `//go:build pi5_hardware`), which returned `temp≈48.3'C` through the Go
  reimplementation of the transport.
