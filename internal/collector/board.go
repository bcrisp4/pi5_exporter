package collector

import (
	"errors"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bcrisp4/pi5_exporter/internal/parse"
	"github.com/bcrisp4/pi5_exporter/internal/platform"
)

var boardInfoDesc = prometheus.NewDesc("pi5_board_info",
	"Board identity as labels; the value is always 1.",
	[]string{"model", "soc", "firmware_hash", "firmware_variant", "serial"}, nil)

type boardCollector struct {
	gc GenCmder
	fs FileReader
}

func newBoardCollector(d Deps) (Collector, error) {
	return &boardCollector{gc: d.GenCmd, fs: d.FS}, nil
}

func (c *boardCollector) Name() string { return "board" }

func (c *boardCollector) Update() ([]prometheus.Metric, error) {
	model := c.dtString("/proc/device-tree/model")
	// Require model: an info metric with all-empty labels would assert identity
	// it doesn't have while reporting success. Fail instead (drop-on-fail),
	// consistent with the rtc/watchdog collectors.
	if model == "" {
		return nil, errors.New("board: no device-tree model; identity unavailable")
	}
	serial := c.dtString("/proc/device-tree/serial-number")

	var soc string
	if compatible, err := c.fs("/proc/device-tree/compatible"); err == nil {
		soc = platform.DetectFamily(compatible).SoC
	}

	var hash, variant string
	if vout, err := c.gc.GenCmd("version"); err == nil {
		if v, err := parse.ParseVersion(vout); err == nil {
			hash, variant = v.Hash, v.Variant
		}
	}

	return []prometheus.Metric{
		prometheus.MustNewConstMetric(boardInfoDesc, prometheus.GaugeValue, 1, model, soc, hash, variant, serial),
	}, nil
}

// dtString reads a device-tree string property, trimming the trailing NUL that
// /proc/device-tree values carry. Returns "" on any error.
func (c *boardCollector) dtString(path string) string {
	b, err := c.fs(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
}
