package collector

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var clockDesc = prometheus.NewDesc("pi5_clock_hertz",
	"Firmware-measured clock frequency by domain (measure_clock). A value of 0 means the "+
		"domain is idle / clock-gated, not broken.", []string{"domain"}, nil)

// clockDomains are the measure_clock domains. Several (h264, pixel, pwm, vec,
// dpi) commonly read 0 when idle; we emit them anyway so absence vs zero is
// unambiguous. "arm" overlaps node_exporter's cpufreq but is the firmware-measured
// rate; included because a standalone user reasonably expects it.
var clockDomains = []string{
	"arm", "core", "v3d", "isp", "h264", "hevc", "pixel", "hdmi", "emmc", "uart", "pwm", "vec", "dpi",
}

type clockCollector struct {
	gc  GenCmder
	log *slog.Logger
}

func newClockCollector(d Deps) (Collector, error) {
	return &clockCollector{gc: d.GenCmd, log: d.Logger}, nil
}

func (c *clockCollector) Name() string { return "clock" }

func (c *clockCollector) Update() ([]prometheus.Metric, error) {
	metrics := make([]prometheus.Metric, 0, len(clockDomains))
	for _, dom := range clockDomains {
		out, err := c.gc.GenCmd("measure_clock " + dom)
		if err != nil {
			return nil, err
		}
		hz, err := parse.ParseClockHertz(out)
		if err != nil {
			c.log.Warn("skipping clock domain", "domain", dom, "output", out, "err", err)
			continue
		}
		metrics = append(metrics, prometheus.MustNewConstMetric(clockDesc, prometheus.GaugeValue, hz, dom))
	}
	return metrics, nil
}
