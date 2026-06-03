// Package collector contains the Raspberry Pi 5 specific metric collectors and
// the master collector that runs them.
//
// Each metric group is a small [Collector] (Name + Update). The master
// [Pi5Collector] runs the enabled ones, timing each and emitting per-collector
// success/duration/last-success meta-metrics. A collector that fails has its
// data series dropped (they go absent) rather than replaying stale values — the
// failure is signalled by the always-present meta-metrics instead. This is the
// node_exporter pattern.
//
// Collectors never read hardware on an HTTP scrape: the master is only gathered
// by the cache scheduler's ticker (see package cache), and Collect is therefore
// only ever called from that single goroutine.
package collector

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// GenCmder runs a VideoCore firmware "gencmd" string (e.g. "measure_temp") and
// returns the raw ASCII result. Declared here at the point of consumption;
// *mailbox.Client satisfies it.
type GenCmder interface {
	GenCmd(cmd string) (string, error)
}

// FileReader reads a file (sysfs / device-tree). Injected so collectors are
// testable with an in-memory map; the default is os.ReadFile.
type FileReader func(path string) ([]byte, error)

// Deps are the injected side-effect handles a collector may need.
type Deps struct {
	GenCmd GenCmder
	FS     FileReader
	Logger *slog.Logger
}

// Collector is one logical group of Pi-5 metrics. Update performs the reads for
// a single collection cycle and returns the metrics by value, so the master can
// drop them when Update fails.
type Collector interface {
	Name() string
	Update() ([]prometheus.Metric, error)
}

// b2f converts a boolean flag to a 0/1 gauge value.
func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Pi5Collector runs a set of sub-collectors and exposes their metrics plus
// per-collector success/duration/last-success meta-metrics. It implements
// prometheus.Collector.
type Pi5Collector struct {
	collectors []Collector
	logger     *slog.Logger
	now        func() time.Time

	// lastSuccess is the last time each collector succeeded. Only touched from
	// Collect, which the scheduler calls from a single goroutine, so no lock.
	lastSuccess map[string]time.Time

	successDesc     *prometheus.Desc
	durationDesc    *prometheus.Desc
	lastSuccessDesc *prometheus.Desc
}

// NewPi5Collector builds the master collector. now is injected for testability.
func NewPi5Collector(cs []Collector, logger *slog.Logger, now func() time.Time) *Pi5Collector {
	return &Pi5Collector{
		collectors:  cs,
		logger:      logger,
		now:         now,
		lastSuccess: make(map[string]time.Time),
		successDesc: prometheus.NewDesc("pi5_scrape_collector_success",
			"1 if the named collector succeeded on the last collection cycle, 0 otherwise.",
			[]string{"collector"}, nil),
		durationDesc: prometheus.NewDesc("pi5_scrape_collector_duration_seconds",
			"Duration of the named collector's last collection cycle.",
			[]string{"collector"}, nil),
		lastSuccessDesc: prometheus.NewDesc("pi5_scrape_collector_last_success_timestamp_seconds",
			"Unix time of the named collector's last successful collection cycle.",
			[]string{"collector"}, nil),
	}
}

// Describe sends only the meta descriptors (the node_exporter pattern); the
// sub-collectors emit const metrics whose descriptors are not pre-declared.
func (p *Pi5Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- p.successDesc
	ch <- p.durationDesc
	ch <- p.lastSuccessDesc
}

// Collect runs every sub-collector, dropping the metrics of any that fail.
func (p *Pi5Collector) Collect(ch chan<- prometheus.Metric) {
	for _, c := range p.collectors {
		start := p.now()
		metrics, err := c.Update()
		dur := p.now().Sub(start).Seconds()

		ch <- prometheus.MustNewConstMetric(p.durationDesc, prometheus.GaugeValue, dur, c.Name())
		if err != nil {
			p.logger.Error("collector failed", "collector", c.Name(), "err", err)
			ch <- prometheus.MustNewConstMetric(p.successDesc, prometheus.GaugeValue, 0, c.Name())
		} else {
			p.lastSuccess[c.Name()] = p.now()
			ch <- prometheus.MustNewConstMetric(p.successDesc, prometheus.GaugeValue, 1, c.Name())
			for _, m := range metrics {
				ch <- m
			}
		}
		if ts, ok := p.lastSuccess[c.Name()]; ok {
			ch <- prometheus.MustNewConstMetric(p.lastSuccessDesc, prometheus.GaugeValue, float64(ts.Unix()), c.Name())
		}
	}
}
