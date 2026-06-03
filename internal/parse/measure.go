package parse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// voltRe matches `vcgencmd measure_volts` output, e.g. "volt=0.8749V".
var voltRe = regexp.MustCompile(`^volt=([0-9.]+)V$`)

// ParseVolts parses `vcgencmd measure_volts` output of the form "volt=0.8749V"
// and returns the voltage in volts. Command-level error bodies such as
// "bad argument" do not match and produce an error.
func ParseVolts(s string) (float64, error) {
	s = strings.TrimSpace(s)
	m := voltRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("parse volts: unrecognised input %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse volts value %q: %w", m[1], err)
	}
	return v, nil
}

// clockRe matches `vcgencmd measure_clock` output, e.g.
// "frequency(0)=1600020224". The frequency is reported in hertz already.
var clockRe = regexp.MustCompile(`^frequency\(\d+\)=(\d+)$`)

// ParseClockHertz parses `vcgencmd measure_clock` output of the form
// "frequency(0)=1600020224" and returns the frequency in hertz. A reported
// frequency of 0 (e.g. for an idle h264 block) is valid and returns 0.
func ParseClockHertz(s string) (float64, error) {
	s = strings.TrimSpace(s)
	m := clockRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("parse clock: unrecognised input %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse clock value %q: %w", m[1], err)
	}
	return v, nil
}

// tempRe matches `vcgencmd measure_temp` output, e.g. "temp=46.6'C". Note the
// apostrophe before the trailing C, which is how the firmware renders the
// degree symbol.
var tempRe = regexp.MustCompile(`^temp=([0-9.]+)'C$`)

// ParseTempCelsius parses `vcgencmd measure_temp` output of the form
// "temp=46.6'C" and returns the temperature in degrees Celsius.
func ParseTempCelsius(s string) (float64, error) {
	s = strings.TrimSpace(s)
	m := tempRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("parse temp: unrecognised input %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse temp value %q: %w", m[1], err)
	}
	return v, nil
}
