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
	// The firmware (mailbox) collectors require a BCM2712 board and /dev/vcio.
	// sysfs collectors (rtc, watchdog) work regardless.
	var gc collector.GenCmder
	firmwareAvailable := false
	if compatible, err := os.ReadFile("/proc/device-tree/compatible"); err != nil {
		logger.Warn("cannot read device-tree compatible; firmware collectors disabled", "err", err)
	} else if fam := platform.DetectFamily(compatible); !fam.IsBCM2712 {
		logger.Warn("not a BCM2712 (Raspberry Pi 5) board; firmware collectors disabled", "model", fam.Model)
	} else if client, err := mailbox.Open(); err != nil {
		logger.Warn("cannot open /dev/vcio; firmware collectors disabled", "err", err)
	} else {
		gc = client
		firmwareAvailable = true
		defer client.Close()
		logger.Info("firmware mailbox available", "soc", fam.SoC, "model", fam.Model)
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
