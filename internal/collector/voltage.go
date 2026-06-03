package collector

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var voltageDesc = prometheus.NewDesc("pi5_voltage_volts",
	"Firmware-reported voltage by domain (measure_volts).", []string{"domain"}, nil)

// voltageDomains are sent verbatim to measure_volts. They must be exact: an
// unknown domain silently returns the core voltage, so a typo would mislabel data.
var voltageDomains = []string{"core", "sdram_c", "sdram_i", "sdram_p"}

type voltageCollector struct {
	gc  GenCmder
	log *slog.Logger
}

func newVoltageCollector(d Deps) (Collector, error) {
	return &voltageCollector{gc: d.GenCmd, log: d.Logger}, nil
}

func (c *voltageCollector) Name() string { return "voltage" }

func (c *voltageCollector) Update() ([]prometheus.Metric, error) {
	metrics := make([]prometheus.Metric, 0, len(voltageDomains))
	for _, dom := range voltageDomains {
		out, err := c.gc.GenCmd("measure_volts " + dom)
		if err != nil {
			return nil, err // transport/firmware failure: drop the whole collector
		}
		v, err := parse.ParseVolts(out)
		if err != nil {
			c.log.Warn("skipping voltage domain", "domain", dom, "output", out, "err", err)
			continue
		}
		metrics = append(metrics, prometheus.MustNewConstMetric(voltageDesc, prometheus.GaugeValue, v, dom))
	}
	return metrics, nil
}
