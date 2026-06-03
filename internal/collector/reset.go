package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var resetStatusDesc = prometheus.NewDesc("pi5_reset_status",
	"Raw firmware reset status from get_rsts (diagnostic bitfield).", nil, nil)

type resetCollector struct{ gc GenCmder }

func newResetCollector(d Deps) (Collector, error) {
	return &resetCollector{gc: d.GenCmd}, nil
}

func (c *resetCollector) Name() string { return "reset" }

func (c *resetCollector) Update() ([]prometheus.Metric, error) {
	out, err := c.gc.GenCmd("get_rsts")
	if err != nil {
		return nil, err
	}
	rs, err := parse.ParseResetStatus(out)
	if err != nil {
		return nil, err
	}
	return []prometheus.Metric{
		prometheus.MustNewConstMetric(resetStatusDesc, prometheus.GaugeValue, float64(rs)),
	}, nil
}
