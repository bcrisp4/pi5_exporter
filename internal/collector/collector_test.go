package collector

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// --- test doubles ---------------------------------------------------------

// fakeGenCmder returns canned firmware output keyed by the EXACT command string,
// so a test also asserts the precise command a collector sends.
type fakeGenCmder map[string]string

func (f fakeGenCmder) GenCmd(cmd string) (string, error) {
	out, ok := f[cmd]
	if !ok {
		return "", errors.New("unexpected gencmd: " + cmd)
	}
	return out, nil
}

func fakeFS(m map[string]string) FileReader {
	return func(p string) ([]byte, error) {
		v, ok := m[p]
		if !ok {
			return nil, errors.New("no such file: " + p)
		}
		return []byte(v), nil
	}
}

func testDeps(gc GenCmder, fs FileReader) Deps {
	return Deps{GenCmd: gc, FS: fs, Logger: slog.New(slog.DiscardHandler)}
}

// single adapts a Collector to prometheus.Collector for testutil. Describe sends
// nothing (an unchecked collector), which is fine for CollectAndCompare.
type single struct{ c Collector }

func (single) Describe(chan<- *prometheus.Desc) {}
func (s single) Collect(ch chan<- prometheus.Metric) {
	ms, err := s.c.Update()
	if err != nil {
		return
	}
	for _, m := range ms {
		ch <- m
	}
}

func compare(t *testing.T, c Collector, golden string, names ...string) {
	t.Helper()
	if err := testutil.CollectAndCompare(single{c}, strings.NewReader(golden), names...); err != nil {
		t.Fatalf("CollectAndCompare: %v", err)
	}
}

// --- collectors -----------------------------------------------------------

func TestThrottleCollector(t *testing.T) {
	// 0x50005 = bits 0 (UV now), 2 (throttled now), 16 (UV since), 18 (throttled since).
	c, _ := newThrottleCollector(testDeps(fakeGenCmder{"get_throttled": "throttled=0x50005"}, nil))
	const golden = `
# HELP pi5_throttle_flags Raw get_throttled bitfield value (debug; prefer the per-flag pi5_throttle_state metrics).
# TYPE pi5_throttle_flags gauge
pi5_throttle_flags 327685
# HELP pi5_throttle_state Current firmware throttle/under-voltage condition (1 = active), per flag.
# TYPE pi5_throttle_state gauge
pi5_throttle_state{flag="arm_frequency_capped"} 0
pi5_throttle_state{flag="soft_temp_limit"} 0
pi5_throttle_state{flag="throttled"} 1
pi5_throttle_state{flag="under_voltage"} 1
# HELP pi5_throttle_state_since_boot Firmware throttle/under-voltage condition latched at any point since boot (1 = occurred), per flag.
# TYPE pi5_throttle_state_since_boot gauge
pi5_throttle_state_since_boot{flag="arm_frequency_capped"} 0
pi5_throttle_state_since_boot{flag="soft_temp_limit"} 0
pi5_throttle_state_since_boot{flag="throttled"} 1
pi5_throttle_state_since_boot{flag="under_voltage"} 1
`
	compare(t, c, golden, "pi5_throttle_state", "pi5_throttle_state_since_boot", "pi5_throttle_flags")
}

func TestThrottleCollectorError(t *testing.T) {
	c, _ := newThrottleCollector(testDeps(fakeGenCmder{"get_throttled": "bad argument"}, nil))
	if _, err := c.Update(); err == nil {
		t.Fatal("expected a parse error for a command-level error body, got nil")
	}
}

func TestPMICCollector(t *testing.T) {
	// Synthetic rails with exact-float values; the real 26-line parse is covered
	// in internal/parse. FOO/BAR have both channels; EXTONLY is voltage-only and
	// must be excluded from watts and the measured-power sum.
	in := strings.Join([]string{
		"  FOO_A current(0)=0.50000000A",
		"  BAR_A current(1)=0.50000000A",
		"  FOO_V volt(2)=2.00000000V",
		"  BAR_V volt(3)=0.50000000V",
		"  EXTONLY_V volt(4)=4.00000000V",
	}, "\n")
	c, _ := newPMICCollector(testDeps(fakeGenCmder{"pmic_read_adc": in}, nil))
	const golden = `
# HELP pi5_pmic_measured_power_watts Sum of per-rail measured power. This is the sum of independently-measured rails, NOT total board input power (there is no 5V-input current channel; EXT5V and BATT are voltage-only).
# TYPE pi5_pmic_measured_power_watts gauge
pi5_pmic_measured_power_watts 1.25
# HELP pi5_pmic_rail_amperes PMIC-measured rail current in amperes.
# TYPE pi5_pmic_rail_amperes gauge
pi5_pmic_rail_amperes{rail="BAR"} 0.5
pi5_pmic_rail_amperes{rail="FOO"} 0.5
# HELP pi5_pmic_rail_volts PMIC-measured rail voltage in volts.
# TYPE pi5_pmic_rail_volts gauge
pi5_pmic_rail_volts{rail="BAR"} 0.5
pi5_pmic_rail_volts{rail="EXTONLY"} 4
pi5_pmic_rail_volts{rail="FOO"} 2
# HELP pi5_pmic_rail_watts Per-rail power (volts*amps), for rails that expose both a voltage and a current channel.
# TYPE pi5_pmic_rail_watts gauge
pi5_pmic_rail_watts{rail="BAR"} 0.25
pi5_pmic_rail_watts{rail="FOO"} 1
`
	compare(t, c, golden,
		"pi5_pmic_rail_volts", "pi5_pmic_rail_amperes", "pi5_pmic_rail_watts", "pi5_pmic_measured_power_watts")
}

func TestVoltageCollector(t *testing.T) {
	c, _ := newVoltageCollector(testDeps(fakeGenCmder{
		"measure_volts core":    "volt=0.8000V",
		"measure_volts sdram_c": "volt=0.6000V",
		"measure_volts sdram_i": "volt=0.6000V",
		"measure_volts sdram_p": "volt=1.1000V",
	}, nil))
	const golden = `
# HELP pi5_voltage_volts Firmware-reported voltage by domain (measure_volts).
# TYPE pi5_voltage_volts gauge
pi5_voltage_volts{domain="core"} 0.8
pi5_voltage_volts{domain="sdram_c"} 0.6
pi5_voltage_volts{domain="sdram_i"} 0.6
pi5_voltage_volts{domain="sdram_p"} 1.1
`
	compare(t, c, golden, "pi5_voltage_volts")
}

func TestTemperatureCollector(t *testing.T) {
	c, _ := newTemperatureCollector(testDeps(fakeGenCmder{
		"measure_temp":      "temp=46.6'C",
		"measure_temp pmic": "temp=43.7'C",
	}, nil))
	const golden = `
# HELP pi5_pmic_temperature_celsius PMIC die temperature (measure_temp pmic). Not exposed by node_exporter.
# TYPE pi5_pmic_temperature_celsius gauge
pi5_pmic_temperature_celsius 43.7
# HELP pi5_soc_temperature_celsius SoC temperature from the firmware sensor (measure_temp). Overlaps node_exporter's thermal_zone0; provided for standalone use.
# TYPE pi5_soc_temperature_celsius gauge
pi5_soc_temperature_celsius 46.6
`
	compare(t, c, golden, "pi5_soc_temperature_celsius", "pi5_pmic_temperature_celsius")
}

func TestBoardCollector(t *testing.T) {
	gc := fakeGenCmder{"version": "2026/05/11 12:20:02 \nCopyright (c) 2012 Broadcom\nversion 66f33f7e (release) (embedded)"}
	fs := fakeFS(map[string]string{
		"/proc/device-tree/model":         "Raspberry Pi 5 Model B Rev 1.1\x00",
		"/proc/device-tree/compatible":    "raspberrypi,5-model-b\x00brcm,bcm2712\x00",
		"/proc/device-tree/serial-number": "100000001234abcd\x00",
	})
	c, _ := newBoardCollector(testDeps(gc, fs))
	const golden = `
# HELP pi5_board_info Board identity as labels; the value is always 1.
# TYPE pi5_board_info gauge
pi5_board_info{firmware_hash="66f33f7e",firmware_variant="release",model="Raspberry Pi 5 Model B Rev 1.1",serial="100000001234abcd",soc="brcm,bcm2712"} 1
`
	compare(t, c, golden, "pi5_board_info")
}

func TestRTCCollector(t *testing.T) {
	c, _ := newRTCCollector(testDeps(nil, fakeFS(map[string]string{
		rtcBase + "/battery_voltage":      "3282048\n",
		rtcBase + "/charging_voltage":     "0\n",
		rtcBase + "/charging_voltage_min": "1300000\n",
		rtcBase + "/charging_voltage_max": "4400000\n",
	})))
	const golden = `
# HELP pi5_rtc_battery_volts RTC backup-cell (battery/supercap) voltage.
# TYPE pi5_rtc_battery_volts gauge
pi5_rtc_battery_volts 3.282048
# HELP pi5_rtc_charging_volts RTC backup-cell trickle-charge target voltage (0 = charging disabled / no cell fitted).
# TYPE pi5_rtc_charging_volts gauge
pi5_rtc_charging_volts 0
# HELP pi5_rtc_charging_volts_max Maximum configurable RTC trickle-charge voltage.
# TYPE pi5_rtc_charging_volts_max gauge
pi5_rtc_charging_volts_max 4.4
# HELP pi5_rtc_charging_volts_min Minimum configurable RTC trickle-charge voltage.
# TYPE pi5_rtc_charging_volts_min gauge
pi5_rtc_charging_volts_min 1.3
`
	compare(t, c, golden, "pi5_rtc_battery_volts", "pi5_rtc_charging_volts", "pi5_rtc_charging_volts_min", "pi5_rtc_charging_volts_max")
}

func TestRTCCollectorMissingBattery(t *testing.T) {
	c, _ := newRTCCollector(testDeps(nil, fakeFS(map[string]string{})))
	if _, err := c.Update(); err == nil {
		t.Fatal("expected error when battery_voltage is absent")
	}
}

func TestWatchdogCollector(t *testing.T) {
	c, _ := newWatchdogCollector(testDeps(nil, fakeFS(map[string]string{
		watchdogBase + "/bootstatus": "0\n",
		watchdogBase + "/timeout":    "60\n",
	})))
	const golden = `
# HELP pi5_watchdog_bootstatus Hardware watchdog boot status (0 = clean; non-zero = last boot was watchdog-triggered).
# TYPE pi5_watchdog_bootstatus gauge
pi5_watchdog_bootstatus 0
# HELP pi5_watchdog_timeout_seconds Configured hardware watchdog timeout.
# TYPE pi5_watchdog_timeout_seconds gauge
pi5_watchdog_timeout_seconds 60
`
	compare(t, c, golden, "pi5_watchdog_bootstatus", "pi5_watchdog_timeout_seconds")
}

// --- master collector -----------------------------------------------------

type fakeCollector struct {
	name    string
	metrics []prometheus.Metric
	err     error
}

func (f fakeCollector) Name() string                         { return f.name }
func (f fakeCollector) Update() ([]prometheus.Metric, error) { return f.metrics, f.err }

func TestMasterDropOnFail(t *testing.T) {
	testDesc := prometheus.NewDesc("test_metric", "help", nil, nil)
	ok := fakeCollector{name: "ok", metrics: []prometheus.Metric{
		prometheus.MustNewConstMetric(testDesc, prometheus.GaugeValue, 42),
	}}
	bad := fakeCollector{name: "fail", err: errors.New("boom")}

	fixed := time.Unix(1700000000, 0)
	master := NewPi5Collector([]Collector{ok, bad}, slog.New(slog.DiscardHandler), func() time.Time { return fixed })

	const golden = `
# HELP pi5_scrape_collector_duration_seconds Duration of the named collector's last collection cycle.
# TYPE pi5_scrape_collector_duration_seconds gauge
pi5_scrape_collector_duration_seconds{collector="fail"} 0
pi5_scrape_collector_duration_seconds{collector="ok"} 0
# HELP pi5_scrape_collector_last_success_timestamp_seconds Unix time of the named collector's last successful collection cycle.
# TYPE pi5_scrape_collector_last_success_timestamp_seconds gauge
pi5_scrape_collector_last_success_timestamp_seconds{collector="ok"} 1.7e+09
# HELP pi5_scrape_collector_success 1 if the named collector succeeded on the last collection cycle, 0 otherwise.
# TYPE pi5_scrape_collector_success gauge
pi5_scrape_collector_success{collector="fail"} 0
pi5_scrape_collector_success{collector="ok"} 1
# HELP test_metric help
# TYPE test_metric gauge
test_metric 42
`
	// The master forwards sub-collector metrics whose descriptors it does not
	// pre-declare (the node_exporter pattern). That's fine in a normal registry,
	// so gather through one rather than testutil's pedantic registry.
	reg := prometheus.NewRegistry()
	reg.MustRegister(master)
	if err := testutil.GatherAndCompare(reg, strings.NewReader(golden),
		"pi5_scrape_collector_success", "pi5_scrape_collector_duration_seconds",
		"pi5_scrape_collector_last_success_timestamp_seconds", "test_metric"); err != nil {
		t.Fatalf("GatherAndCompare: %v", err)
	}
}

func TestClockCollector(t *testing.T) {
	fake := fakeGenCmder{}
	for _, d := range clockDomains {
		fake["measure_clock "+d] = "frequency(0)=0" // idle/gated emits 0, not an error
	}
	fake["measure_clock arm"] = "frequency(0)=2000000000"
	fake["measure_clock core"] = "frequency(0)=500000000"
	c, _ := newClockCollector(testDeps(fake, nil))
	const golden = `
# HELP pi5_clock_hertz Firmware-measured clock frequency by domain (measure_clock). A value of 0 means the domain is idle / clock-gated, not broken.
# TYPE pi5_clock_hertz gauge
pi5_clock_hertz{domain="arm"} 2e+09
pi5_clock_hertz{domain="core"} 5e+08
pi5_clock_hertz{domain="dpi"} 0
pi5_clock_hertz{domain="emmc"} 0
pi5_clock_hertz{domain="h264"} 0
pi5_clock_hertz{domain="hdmi"} 0
pi5_clock_hertz{domain="hevc"} 0
pi5_clock_hertz{domain="isp"} 0
pi5_clock_hertz{domain="pixel"} 0
pi5_clock_hertz{domain="pwm"} 0
pi5_clock_hertz{domain="uart"} 0
pi5_clock_hertz{domain="v3d"} 0
pi5_clock_hertz{domain="vec"} 0
`
	compare(t, c, golden, "pi5_clock_hertz")
}

func TestRingOscCollector(t *testing.T) {
	c, _ := newRingOscCollector(testDeps(fakeGenCmder{
		"read_ring_osc": "read_ring_osc(2)=9.368MHz (@0.8749V) (46.6'C)",
	}, nil))
	const golden = `
# HELP pi5_ring_osc_hertz Ring oscillator frequency from read_ring_osc (diagnostic).
# TYPE pi5_ring_osc_hertz gauge
pi5_ring_osc_hertz 9.368e+06
`
	compare(t, c, golden, "pi5_ring_osc_hertz")
}

func TestResetCollector(t *testing.T) {
	c, _ := newResetCollector(testDeps(fakeGenCmder{"get_rsts": "get_rsts=1020"}, nil))
	const golden = `
# HELP pi5_reset_status Raw firmware reset status from get_rsts (diagnostic bitfield).
# TYPE pi5_reset_status gauge
pi5_reset_status 1020
`
	compare(t, c, golden, "pi5_reset_status")
}

func TestBoardCollectorMissingModel(t *testing.T) {
	// No device-tree model + failing version: identity is unavailable, so the
	// collector must fail (drop-on-fail) rather than emit an all-empty info metric.
	c, _ := newBoardCollector(testDeps(fakeGenCmder{}, fakeFS(map[string]string{})))
	if _, err := c.Update(); err == nil {
		t.Fatal("expected error when device-tree model is absent")
	}
}

func collectorNames(cs []Collector, _ error) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name())
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestFactoryEnableDisableAndFirmwareGate(t *testing.T) {
	app := kingpin.New("test", "")
	reg := NewRegistry(app)
	if _, err := app.Parse([]string{"--no-collector.pmic"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := testDeps(fakeGenCmder{}, fakeFS(nil))

	// Firmware available: default-on set minus pmic; default-off extras absent.
	on := collectorNames(reg.Build(d, true))
	for _, want := range []string{"throttle", "voltage", "clock", "temperature", "board", "rtc"} {
		if !hasName(on, want) {
			t.Errorf("collector %q should be enabled, got %v", want, on)
		}
	}
	if hasName(on, "pmic") {
		t.Errorf("pmic should be disabled by --no-collector.pmic, got %v", on)
	}
	for _, off := range []string{"watchdog", "ringosc", "reset"} {
		if hasName(on, off) {
			t.Errorf("default-off collector %q should be absent, got %v", off, on)
		}
	}

	// Firmware unavailable: only the sysfs collector (rtc) survives the default set.
	none := collectorNames(reg.Build(d, false))
	for _, fw := range []string{"throttle", "pmic", "voltage", "clock", "temperature", "board"} {
		if hasName(none, fw) {
			t.Errorf("firmware collector %q should be skipped when firmware unavailable, got %v", fw, none)
		}
	}
	if !hasName(none, "rtc") {
		t.Errorf("rtc (sysfs) should remain when firmware unavailable, got %v", none)
	}
}
