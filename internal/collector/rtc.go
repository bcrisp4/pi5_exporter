package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

// rtcBase is the stable class path that symlinks to the rpi_rtc device.
const rtcBase = "/sys/class/rtc/rtc0"

var (
	rtcBatteryDesc = prometheus.NewDesc("pi5_rtc_battery_volts",
		"RTC backup-cell (battery/supercap) voltage.", nil, nil)
	rtcChargingDesc = prometheus.NewDesc("pi5_rtc_charging_volts",
		"RTC backup-cell trickle-charge target voltage (0 = charging disabled / no cell fitted).", nil, nil)
	rtcChargingMinDesc = prometheus.NewDesc("pi5_rtc_charging_volts_min",
		"Minimum configurable RTC trickle-charge voltage.", nil, nil)
	rtcChargingMaxDesc = prometheus.NewDesc("pi5_rtc_charging_volts_max",
		"Maximum configurable RTC trickle-charge voltage.", nil, nil)
)

type rtcCollector struct {
	fs   FileReader
	base string
}

func newRTCCollector(d Deps) (Collector, error) {
	return &rtcCollector{fs: d.FS, base: rtcBase}, nil
}

func (c *rtcCollector) Name() string { return "rtc" }

func (c *rtcCollector) Update() ([]prometheus.Metric, error) {
	// battery_voltage is required: if it's missing this isn't a Pi 5 RTC and the
	// collector should report failure (success=0) rather than emit nothing silently.
	bat, err := c.readVolts("battery_voltage")
	if err != nil {
		return nil, err
	}
	metrics := []prometheus.Metric{
		prometheus.MustNewConstMetric(rtcBatteryDesc, prometheus.GaugeValue, bat),
	}

	// The charging attributes are best-effort.
	for _, e := range []struct {
		file string
		desc *prometheus.Desc
	}{
		{"charging_voltage", rtcChargingDesc},
		{"charging_voltage_min", rtcChargingMinDesc},
		{"charging_voltage_max", rtcChargingMaxDesc},
	} {
		if v, err := c.readVolts(e.file); err == nil {
			metrics = append(metrics, prometheus.MustNewConstMetric(e.desc, prometheus.GaugeValue, v))
		}
	}
	return metrics, nil
}

// readVolts reads a microvolt sysfs attribute and returns volts.
func (c *rtcCollector) readVolts(file string) (float64, error) {
	b, err := c.fs(c.base + "/" + file)
	if err != nil {
		return 0, err
	}
	uv, err := parse.ParseSysfsInt(string(b))
	if err != nil {
		return 0, err
	}
	return float64(uv) / 1e6, nil
}
