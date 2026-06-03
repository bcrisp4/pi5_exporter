// Command pi5_exporter is a Prometheus exporter for Raspberry Pi 5 (Broadcom
// BCM2712) firmware/mailbox telemetry that node_exporter does not cover: PMIC
// per-rail power, decoded throttle/under-voltage state, firmware voltages and
// clocks, SoC/PMIC temperature, and the RTC backup-cell voltage.
//
// Metrics are collected on an internal interval and cached; the /metrics endpoint
// serves the latest cached snapshot and never reads hardware on a scrape.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/promslog"
	promslogflag "github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/bcrisp4/pi5_exporter/internal/cache"
	"github.com/bcrisp4/pi5_exporter/internal/collector"
	"github.com/bcrisp4/pi5_exporter/internal/mailbox"
	"github.com/bcrisp4/pi5_exporter/internal/platform"
)

func main() {
	app := kingpin.New("pi5_exporter",
		"Prometheus exporter for Raspberry Pi 5 (BCM2712) firmware metrics not covered by node_exporter.")

	collectorRegistry := collector.NewRegistry(app)
	webFlags := kingpinflag.AddFlags(app, ":2712")
	interval := app.Flag("collection.interval",
		"How often to collect metrics from the hardware. /metrics serves the latest cached values, "+
			"so set Prometheus scrape_interval >= this.").Default("15s").Duration()

	promslogConfig := &promslog.Config{}
	promslogflag.AddFlags(app, promslogConfig)
	app.Version(version.Print("pi5_exporter"))
	app.HelpFlag.Short('h')
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logger := promslog.New(promslogConfig)
	logger.Info("starting pi5_exporter", "version", version.Info(), "build_context", version.BuildContext())

	// --- Firmware availability gate ---------------------------------------
	// /dev/vcio is the authoritative signal; the device-tree compatible string is
	// only a best-effort negative check (see resolveFirmware). sysfs collectors
	// (rtc, watchdog) run regardless.
	client, firmwareAvailable := resolveFirmware(
		func() ([]byte, error) { return os.ReadFile("/proc/device-tree/compatible") },
		func() (firmwareClient, error) {
			c, err := mailbox.Open()
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		logger,
	)
	var gc collector.GenCmder
	if firmwareAvailable {
		gc = client
		defer func() { _ = client.Close() }()
	}

	deps := collector.Deps{GenCmd: gc, FS: os.ReadFile, Logger: logger}
	collectors, err := collectorRegistry.Build(deps, firmwareAvailable)
	if err != nil {
		logger.Error("failed to build collectors", "err", err)
		os.Exit(1)
	}

	// --- Internal registry (gathered only by the scheduler) ----------------
	internal := prometheus.NewRegistry()
	internal.MustRegister(versioncollector.NewCollector("pi5_exporter"))
	internal.MustRegister(collector.NewPi5Collector(collectors, logger, time.Now))

	// --- Cache + scheduler -------------------------------------------------
	c := &cache.Cache{}
	scheduler := cache.NewScheduler(internal, c, time.Now, logger)
	// Eager first collection BEFORE serving, so the first scrape is never empty.
	if err := scheduler.CollectOnce(); err != nil {
		logger.Warn("initial collection completed with errors", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go scheduler.Run(ctx, *interval)

	// --- HTTP server -------------------------------------------------------
	servingGatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		return c.Gather(time.Now)
	})
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(servingGatherer, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))
	if landing, err := web.NewLandingPage(web.LandingConfig{
		Name:        "pi5_exporter",
		Description: "Raspberry Pi 5 (BCM2712) firmware metrics exporter",
		Version:     version.Info(),
		Links:       []web.LandingLinks{{Address: "/metrics", Text: "Metrics"}},
	}); err != nil {
		logger.Warn("landing page disabled", "err", err)
	} else {
		mux.Handle("/", landing)
	}

	srv := &http.Server{
		Handler: mux,
		// Bound the header read so a slow client can't hold a connection open
		// indefinitely (gosec G112 / Slowloris).
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// Graceful drain: metrics are cached so serving is sub-millisecond, and
		// letting an in-flight scrape finish is cheap and idiomatic.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := web.ListenAndServe(srv, webFlags, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "err", err)
		os.Exit(1)
	}
}

// firmwareClient is the firmware mailbox handle: a GenCmder that can be closed.
// *mailbox.Client satisfies it.
type firmwareClient interface {
	collector.GenCmder
	Close() error
}

// resolveFirmware decides whether the firmware (mailbox) collectors can run.
//
// The authoritative signal is whether /dev/vcio opens (openMailbox). The
// device-tree "compatible" string (readCompatible) is only a best-effort EARLY
// check: a board we can positively identify as non-BCM2712 (e.g. a Pi 4) is
// disabled up front. Crucially an UNREADABLE device-tree — e.g. inside a
// container where /proc/device-tree is not mounted — is treated as "unknown",
// not "unavailable": we fall through and let /dev/vcio be the judge. That is why
// the firmware metrics work in a container with only --device /dev/vcio and no
// device-tree mount.
func resolveFirmware(
	readCompatible func() ([]byte, error),
	openMailbox func() (firmwareClient, error),
	logger *slog.Logger,
) (firmwareClient, bool) {
	if compatible, err := readCompatible(); err == nil {
		if fam := platform.DetectFamily(compatible); !fam.IsBCM2712 {
			logger.Warn("not a BCM2712 (Raspberry Pi 5) board; firmware collectors disabled", "model", fam.Model)
			return nil, false
		}
	}
	client, err := openMailbox()
	if err != nil {
		logger.Warn("cannot open /dev/vcio; firmware collectors disabled", "err", err)
		return nil, false
	}
	logger.Info("firmware mailbox available")
	return client, true
}
