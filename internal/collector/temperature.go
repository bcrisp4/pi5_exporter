package collector

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var (
	socTempDesc = prometheus.NewDesc("pi5_soc_temperature_celsius",
		"SoC temperature from the firmware sensor (measure_temp). Overlaps node_exporter's "+
			"thermal_zone0; provided for standalone use.", nil, nil)
	pmicTempDesc = prometheus.NewDesc("pi5_pmic_temperature_celsius",
		"PMIC die temperature (measure_temp pmic). Not exposed by node_exporter.", nil, nil)
)

type temperatureCollector struct {
	gc  GenCmder
	log *slog.Logger
}

func newTemperatureCollector(d Deps) (Collector, error) {
	return &temperatureCollector{gc: d.GenCmd, log: d.Logger}, nil
}

func (c *temperatureCollector) Name() string { return "temperature" }

func (c *temperatureCollector) Update() ([]prometheus.Metric, error) {
	out, err := c.gc.GenCmd("measure_temp")
	if err != nil {
		return nil, err
	}
	soc, err := parse.ParseTempCelsius(out)
	if err != nil {
		return nil, err
	}
	metrics := []prometheus.Metric{
		prometheus.MustNewConstMetric(socTempDesc, prometheus.GaugeValue, soc),
	}

	// PMIC die temperature is best-effort: skip it (rather than fail the whole
	// collector) if unavailable.
	if pout, err := c.gc.GenCmd("measure_temp pmic"); err != nil {
		c.log.Warn("skipping pmic temperature", "err", err)
	} else if pt, err := parse.ParseTempCelsius(pout); err != nil {
		c.log.Warn("skipping pmic temperature", "output", pout, "err", err)
	} else {
		metrics = append(metrics, prometheus.MustNewConstMetric(pmicTempDesc, prometheus.GaugeValue, pt))
	}
	return metrics, nil
}
