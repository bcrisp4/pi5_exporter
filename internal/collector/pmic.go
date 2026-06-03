package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
)

var (
	pmicVoltsDesc = prometheus.NewDesc("pi5_pmic_rail_volts",
		"PMIC-measured rail voltage in volts.", []string{"rail"}, nil)
	pmicAmpsDesc = prometheus.NewDesc("pi5_pmic_rail_amperes",
		"PMIC-measured rail current in amperes.", []string{"rail"}, nil)
	pmicWattsDesc = prometheus.NewDesc("pi5_pmic_rail_watts",
		"Per-rail power (volts*amps), for rails that expose both a voltage and a current channel.",
		[]string{"rail"}, nil)
	pmicTotalDesc = prometheus.NewDesc("pi5_pmic_measured_power_watts",
		"Sum of per-rail measured power. This is the sum of independently-measured rails, "+
			"NOT total board input power (there is no 5V-input current channel; EXT5V and BATT are voltage-only).",
		nil, nil)
)

type pmicCollector struct{ gc GenCmder }

func newPMICCollector(d Deps) (Collector, error) {
	return &pmicCollector{gc: d.GenCmd}, nil
}

func (c *pmicCollector) Name() string { return "pmic" }

func (c *pmicCollector) Update() ([]prometheus.Metric, error) {
	out, err := c.gc.GenCmd("pmic_read_adc")
	if err != nil {
		return nil, err
	}
	rails, err := parse.ParsePMIC(out)
	if err != nil {
		return nil, err
	}

	metrics := make([]prometheus.Metric, 0, len(rails)*3+1)
	var total float64
	for _, r := range rails {
		if r.HasVolts {
			metrics = append(metrics, prometheus.MustNewConstMetric(pmicVoltsDesc, prometheus.GaugeValue, r.Volts, r.Name))
		}
		if r.HasAmps {
			metrics = append(metrics, prometheus.MustNewConstMetric(pmicAmpsDesc, prometheus.GaugeValue, r.Amps, r.Name))
		}
		if r.HasVolts && r.HasAmps {
			w := r.Volts * r.Amps
			metrics = append(metrics, prometheus.MustNewConstMetric(pmicWattsDesc, prometheus.GaugeValue, w, r.Name))
			total += w
		}
	}
	metrics = append(metrics, prometheus.MustNewConstMetric(pmicTotalDesc, prometheus.GaugeValue, total))
	return metrics, nil
}
