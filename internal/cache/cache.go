// Package cache implements the collect-on-an-interval / serve-from-cache model:
// a background scheduler gathers metrics every interval into an atomically-swapped
// snapshot, and the HTTP handler serves that snapshot. Hardware is therefore never
// read on a scrape — only on a tick.
package cache

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// Gatherer is the subset of prometheus.Gatherer the cache needs. Declared here
// at the point of consumption; a *prometheus.Registry satisfies it.
type Gatherer interface {
	Gather() ([]*dto.MetricFamily, error)
}

// Snapshot is one immutable collection result.
type Snapshot struct {
	Families    []*dto.MetricFamily
	CollectedAt time.Time
	Duration    time.Duration
}

// Cache holds the latest Snapshot for lock-free reads.
type Cache struct {
	v atomic.Pointer[Snapshot]
}

// Store atomically replaces the cached snapshot.
func (c *Cache) Store(s *Snapshot) { c.v.Store(s) }

// Load returns the current snapshot, or nil before the first collection.
func (c *Cache) Load() *Snapshot { return c.v.Load() }

// Gather serves the cached families plus freshly-computed staleness meta-metrics
// (age is computed at call time, so it stays truthful between ticks). It never
// triggers a collection. Satisfies prometheus.GathererFunc when wrapped.
func (c *Cache) Gather(now func() time.Time) ([]*dto.MetricFamily, error) {
	snap := c.Load()
	if snap == nil {
		return nil, nil // before the first (eager) collection
	}

	// Age is computed at serve time so it stays truthful between ticks. Clamp to
	// >= 0: a backward wall-clock step (NTP) must not produce a negative gauge
	// that would break "age > N" staleness alerts.
	age := now().Sub(snap.CollectedAt).Seconds()
	if age < 0 {
		age = 0
	}

	out := make([]*dto.MetricFamily, 0, len(snap.Families)+3)
	out = append(out, snap.Families...)

	// Append the staleness meta families, but never a name already in the
	// snapshot: the served gatherer is a raw GathererFunc with no registry to
	// dedup, so a duplicate family name would emit two HELP/TYPE blocks and the
	// Prometheus parser would reject the whole scrape.
	have := make(map[string]struct{}, len(snap.Families))
	for _, f := range snap.Families {
		have[f.GetName()] = struct{}{}
	}
	for _, mf := range []*dto.MetricFamily{
		gaugeFamily("pi5_exporter_metrics_age_seconds",
			"Seconds since the currently-served metrics were collected.", age),
		gaugeFamily("pi5_exporter_last_collection_timestamp_seconds",
			"Unix time at which the currently-served metrics were collected.",
			float64(snap.CollectedAt.Unix())),
		gaugeFamily("pi5_exporter_last_collection_duration_seconds",
			"Duration of the collection cycle that produced the currently-served metrics.",
			snap.Duration.Seconds()),
	} {
		if _, dup := have[mf.GetName()]; !dup {
			out = append(out, mf)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

// Scheduler gathers from src into a Cache on an interval.
type Scheduler struct {
	src    Gatherer
	cache  *Cache
	now    func() time.Time
	logger *slog.Logger
}

// NewScheduler wires a scheduler. now is injected for testability.
func NewScheduler(src Gatherer, cache *Cache, now func() time.Time, logger *slog.Logger) *Scheduler {
	return &Scheduler{src: src, cache: cache, now: now, logger: logger}
}

// CollectOnce gathers once and stores the snapshot. A gather error is logged but
// any partial families returned are still stored. Returns the gather error (used
// by the eager startup collection to surface problems).
func (s *Scheduler) CollectOnce() error {
	start := s.now()
	fams, err := s.src.Gather()
	if err != nil {
		s.logger.Error("metric gather returned an error (storing partial result)", "err", err)
	}
	end := s.now()
	s.cache.Store(&Snapshot{Families: fams, CollectedAt: end, Duration: end.Sub(start)})
	return err
}

// Run collects every interval until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.CollectOnce()
		}
	}
}

// gaugeFamily builds a single-sample untyped-gauge MetricFamily.
func gaugeFamily(name, help string, val float64) *dto.MetricFamily {
	mt := dto.MetricType_GAUGE
	return &dto.MetricFamily{
		Name:   &name,
		Help:   &help,
		Type:   &mt,
		Metric: []*dto.Metric{{Gauge: &dto.Gauge{Value: &val}}},
	}
}
