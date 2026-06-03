package collector

import (
	"fmt"
	"strconv"

	"github.com/alecthomas/kingpin/v2"
)

// registration is one collector's flag + constructor.
type registration struct {
	name      string
	defaultOn bool
	// firmware is true when the collector needs the VideoCore mailbox; such
	// collectors are skipped when the firmware is unavailable (non-Pi-5 / no /dev/vcio).
	firmware bool
	build    func(Deps) (Collector, error)
	enabled  *bool
}

// Registry holds the known collectors and their --collector.<name> flags. It has
// no package-level state: the flags live on the injected kingpin application.
type Registry struct {
	regs []*registration
}

// NewRegistry registers every collector and binds --collector.<name> /
// --no-collector.<name> with per-collector defaults. Call after constructing the
// kingpin application and before Parse.
func NewRegistry(app *kingpin.Application) *Registry {
	r := &Registry{}
	add := func(name string, defaultOn, firmware bool, build func(Deps) (Collector, error)) {
		enabled := app.Flag("collector."+name,
			fmt.Sprintf("Enable the %s collector (default %t).", name, defaultOn)).
			Default(strconv.FormatBool(defaultOn)).Bool()
		r.regs = append(r.regs, &registration{name, defaultOn, firmware, build, enabled})
	}

	// Default-ON: the Pi-5-specific firmware metrics + RTC.
	add("throttle", true, true, newThrottleCollector)
	add("pmic", true, true, newPMICCollector)
	add("voltage", true, true, newVoltageCollector)
	add("clock", true, true, newClockCollector)
	add("temperature", true, true, newTemperatureCollector)
	add("board", true, true, newBoardCollector)
	add("rtc", true, false, newRTCCollector)
	// Default-OFF extras.
	add("watchdog", false, false, newWatchdogCollector)
	add("ringosc", false, true, newRingOscCollector)
	add("reset", false, true, newResetCollector)

	return r
}

// Build instantiates the enabled collectors. firmwareAvailable gates the
// firmware (mailbox) collectors; when false they are logged and skipped.
func (r *Registry) Build(d Deps, firmwareAvailable bool) ([]Collector, error) {
	var out []Collector
	for _, reg := range r.regs {
		if !*reg.enabled {
			continue
		}
		if reg.firmware && !firmwareAvailable {
			d.Logger.Warn("collector disabled: firmware/mailbox unavailable", "collector", reg.name)
			continue
		}
		c, err := reg.build(d)
		if err != nil {
			return nil, fmt.Errorf("build collector %s: %w", reg.name, err)
		}
		out = append(out, c)
	}
	return out, nil
}
