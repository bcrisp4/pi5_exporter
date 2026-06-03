package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var ringOscDesc = prometheus.NewDesc("pi5_ring_osc_hertz",
	"Ring oscillator frequency from read_ring_osc (diagnostic).", nil, nil)

type ringOscCollector struct{ gc GenCmder }

func newRingOscCollector(d Deps) (Collector, error) {
	return &ringOscCollector{gc: d.GenCmd}, nil
}

func (c *ringOscCollector) Name() string { return "ringosc" }

func (c *ringOscCollector) Update() ([]prometheus.Metric, error) {
	out, err := c.gc.GenCmd("read_ring_osc")
	if err != nil {
		return nil, err
	}
	ro, err := parse.ParseRingOsc(out)
	if err != nil {
		return nil, err
	}
	return []prometheus.Metric{
		prometheus.MustNewConstMetric(ringOscDesc, prometheus.GaugeValue, ro.Hertz),
	}, nil
}
