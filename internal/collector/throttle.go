package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var (
	throttleStateDesc = prometheus.NewDesc("pi5_throttle_state",
		"Current firmware throttle/under-voltage condition (1 = active), per flag.",
		[]string{"flag"}, nil)
	throttleSinceDesc = prometheus.NewDesc("pi5_throttle_state_since_boot",
		"Firmware throttle/under-voltage condition latched at any point since boot (1 = occurred), per flag.",
		[]string{"flag"}, nil)
	throttleFlagsDesc = prometheus.NewDesc("pi5_throttle_flags",
		"Raw get_throttled bitfield value (debug; prefer the per-flag pi5_throttle_state metrics).",
		nil, nil)
)

type throttleCollector struct{ gc GenCmder }

func newThrottleCollector(d Deps) (Collector, error) {
	return &throttleCollector{gc: d.GenCmd}, nil
}

func (c *throttleCollector) Name() string { return "throttle" }

func (c *throttleCollector) Update() ([]prometheus.Metric, error) {
	out, err := c.gc.GenCmd("get_throttled")
	if err != nil {
		return nil, err
	}
	t, err := parse.ParseThrottled(out)
	if err != nil {
		return nil, err
	}

	flags := []struct {
		name       string
		now, since bool
	}{
		{"under_voltage", t.UnderVoltageNow, t.UnderVoltageSince},
		{"arm_frequency_capped", t.ArmFreqCappedNow, t.ArmFreqCappedSince},
		{"throttled", t.ThrottledNow, t.ThrottledSince},
		{"soft_temp_limit", t.SoftTempLimitNow, t.SoftTempLimitSince},
	}

	metrics := make([]prometheus.Metric, 0, len(flags)*2+1)
	for _, f := range flags {
		metrics = append(metrics,
			prometheus.MustNewConstMetric(throttleStateDesc, prometheus.GaugeValue, b2f(f.now), f.name),
			prometheus.MustNewConstMetric(throttleSinceDesc, prometheus.GaugeValue, b2f(f.since), f.name),
		)
	}
	metrics = append(metrics, prometheus.MustNewConstMetric(throttleFlagsDesc, prometheus.GaugeValue, float64(t.Raw)))
	return metrics, nil
}
