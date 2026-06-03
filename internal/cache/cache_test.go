package cache

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// countingGatherer records how many times Gather is called, so we can prove that
// serving /metrics never triggers a collection.
type countingGatherer struct {
	fams  []*dto.MetricFamily
	calls int
}

func (g *countingGatherer) Gather() ([]*dto.MetricFamily, error) {
	g.calls++
	return g.fams, nil
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func familyValue(fams []*dto.MetricFamily, name string) (float64, bool) {
	for _, f := range fams {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func TestCollectOnceStoresSnapshot(t *testing.T) {
	g := &countingGatherer{fams: []*dto.MetricFamily{gaugeFamily("x_value", "h", 1)}}
	c := &Cache{}
	at := time.Unix(1700000000, 0)
	s := NewScheduler(g, c, fixedClock(at), slog.New(slog.DiscardHandler))

	if err := s.CollectOnce(); err != nil {
		t.Fatalf("CollectOnce: %v", err)
	}
	snap := c.Load()
	if snap == nil {
		t.Fatal("snapshot not stored")
	}
	if len(snap.Families) != 1 || snap.Families[0].GetName() != "x_value" {
		t.Fatalf("unexpected families: %+v", snap.Families)
	}
	if !snap.CollectedAt.Equal(at) {
		t.Fatalf("CollectedAt = %v, want %v", snap.CollectedAt, at)
	}
}

// TestServingNeverCollects is the regression guard for the cache requirement:
// serving the cached snapshot must not call the underlying gatherer.
func TestServingNeverCollects(t *testing.T) {
	g := &countingGatherer{fams: []*dto.MetricFamily{gaugeFamily("x_value", "h", 1)}}
	c := &Cache{}
	at := time.Unix(1700000000, 0)
	s := NewScheduler(g, c, fixedClock(at), slog.New(slog.DiscardHandler))

	_ = s.CollectOnce() // the only collection
	for i := 0; i < 5; i++ {
		if _, err := c.Gather(fixedClock(at)); err != nil {
			t.Fatalf("Gather: %v", err)
		}
	}
	if g.calls != 1 {
		t.Fatalf("underlying gatherer called %d times, want 1 (scrapes must not collect)", g.calls)
	}
}

func TestGatherAppendsStalenessMeta(t *testing.T) {
	g := &countingGatherer{fams: []*dto.MetricFamily{gaugeFamily("x_value", "h", 1)}}
	c := &Cache{}
	at := time.Unix(1700000000, 0)
	s := NewScheduler(g, c, fixedClock(at), slog.New(slog.DiscardHandler))
	_ = s.CollectOnce()

	// Serve 3 seconds later: age must reflect the elapsed time, computed at serve time.
	out, err := c.Gather(fixedClock(at.Add(3 * time.Second)))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if v, ok := familyValue(out, "pi5_exporter_metrics_age_seconds"); !ok || v != 3 {
		t.Fatalf("metrics_age_seconds = %v (ok=%v), want 3", v, ok)
	}
	if v, ok := familyValue(out, "pi5_exporter_last_collection_timestamp_seconds"); !ok || v != 1.7e9 {
		t.Fatalf("last_collection_timestamp_seconds = %v (ok=%v), want 1.7e9", v, ok)
	}
	if _, ok := familyValue(out, "x_value"); !ok {
		t.Fatal("cached family x_value missing from served output")
	}
}

func TestGatherBeforeFirstCollection(t *testing.T) {
	c := &Cache{}
	out, err := c.Gather(fixedClock(time.Unix(1700000000, 0)))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil before first collection, got %d families", len(out))
	}
}

// TestGatherClampsNegativeAge guards against a backward wall-clock step (NTP)
// producing a negative staleness gauge.
func TestGatherClampsNegativeAge(t *testing.T) {
	g := &countingGatherer{fams: []*dto.MetricFamily{gaugeFamily("x_value", "h", 1)}}
	c := &Cache{}
	at := time.Unix(1700000000, 0)
	_ = NewScheduler(g, c, fixedClock(at), slog.New(slog.DiscardHandler)).CollectOnce()

	out, err := c.Gather(fixedClock(at.Add(-10 * time.Second)))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if v, ok := familyValue(out, "pi5_exporter_metrics_age_seconds"); !ok || v != 0 {
		t.Fatalf("metrics_age_seconds = %v (ok=%v), want clamped to 0", v, ok)
	}
}

// TestHandlerServesCacheNoRecollectNoDupes is the end-to-end guard for the cache
// requirement and the meta-family dedup: N scrapes trigger 0 extra collections,
// and a collector whose name collides with a meta family must not produce two
// HELP/TYPE blocks (which would make the whole scrape invalid).
func TestHandlerServesCacheNoRecollectNoDupes(t *testing.T) {
	g := &countingGatherer{fams: []*dto.MetricFamily{
		gaugeFamily("x_value", "h", 1),
		// Deliberate collision with one of the appended meta families.
		gaugeFamily("pi5_exporter_metrics_age_seconds", "collision from a collector", 99),
	}}
	c := &Cache{}
	at := time.Unix(1700000000, 0)
	_ = NewScheduler(g, c, fixedClock(at), slog.New(slog.DiscardHandler)).CollectOnce()

	h := promhttp.HandlerFor(prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		return c.Gather(fixedClock(at))
	}), promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})

	var body string
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		if rec.Code != 200 {
			t.Fatalf("scrape %d: status %d, body=%q", i, rec.Code, rec.Body.String())
		}
		body = rec.Body.String()
	}
	if g.calls != 1 {
		t.Fatalf("underlying gatherer called %d times across 5 scrapes, want 1", g.calls)
	}

	help := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# HELP ") {
			help[strings.Fields(line)[2]]++
		}
	}
	for name, n := range help {
		if n != 1 {
			t.Errorf("metric %q has %d HELP lines, want exactly 1", name, n)
		}
	}
	if help["pi5_exporter_metrics_age_seconds"] != 1 {
		t.Errorf("age meta-metric not served exactly once: %d", help["pi5_exporter_metrics_age_seconds"])
	}
}
