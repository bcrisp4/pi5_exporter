package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

const watchdogBase = "/sys/class/watchdog/watchdog0"

var (
	watchdogBootstatusDesc = prometheus.NewDesc("pi5_watchdog_bootstatus",
		"Hardware watchdog boot status (0 = clean; non-zero = last boot was watchdog-triggered).", nil, nil)
	watchdogTimeoutDesc = prometheus.NewDesc("pi5_watchdog_timeout_seconds",
		"Configured hardware watchdog timeout.", nil, nil)
)

type watchdogCollector struct {
	fs   FileReader
	base string
}

func newWatchdogCollector(d Deps) (Collector, error) {
	return &watchdogCollector{fs: d.FS, base: watchdogBase}, nil
}

func (c *watchdogCollector) Name() string { return "watchdog" }

func (c *watchdogCollector) Update() ([]prometheus.Metric, error) {
	boot, err := c.readInt("bootstatus")
	if err != nil {
		return nil, err
	}
	metrics := []prometheus.Metric{
		prometheus.MustNewConstMetric(watchdogBootstatusDesc, prometheus.GaugeValue, float64(boot)),
	}
	if t, err := c.readInt("timeout"); err == nil {
		metrics = append(metrics, prometheus.MustNewConstMetric(watchdogTimeoutDesc, prometheus.GaugeValue, float64(t)))
	}
	return metrics, nil
}

func (c *watchdogCollector) readInt(file string) (int64, error) {
	b, err := c.fs(c.base + "/" + file)
	if err != nil {
		return 0, err
	}
	return parse.ParseSysfsInt(string(b))
}
