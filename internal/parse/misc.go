package parse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is the decoded form of the `vcgencmd version` block.
type Version struct {
	// FirmwareDate is the first line of the block if it looks like a
	// timestamp, otherwise empty.
	FirmwareDate string
	Hash         string
	Variant      string
	Build        string
}

// versionLineRe matches the last line of a `vcgencmd version` block, e.g.
// "version 66f33f7e (release) (embedded)".
var versionLineRe = regexp.MustCompile(`^version\s+(\S+)\s+\((\S+)\)\s+\((\S+)\)$`)

// firmwareDateRe loosely matches a leading "YYYY/MM/DD HH:MM:SS" timestamp such
// as "2026/05/11 12:20:02" that the firmware prints as the first line.
var firmwareDateRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}$`)

// ParseVersion parses the multi-line `vcgencmd version` block. The last
// non-empty line is expected to be "version <hash> (<variant>) (<build>)".
// FirmwareDate is taken from the first non-empty line when it looks like a
// timestamp, otherwise it is left empty.
func ParseVersion(s string) (Version, error) {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) == 0 {
		return Version{}, fmt.Errorf("parse version: empty input")
	}

	last := lines[len(lines)-1]
	m := versionLineRe.FindStringSubmatch(last)
	if m == nil {
		return Version{}, fmt.Errorf("parse version: unrecognised version line %q", last)
	}

	v := Version{
		Hash:    m[1],
		Variant: m[2],
		Build:   m[3],
	}
	if first := lines[0]; firmwareDateRe.MatchString(first) {
		v.FirmwareDate = first
	}
	return v, nil
}

// ParseSysfsInt parses an integer from a sysfs/hwmon file, trimming any
// surrounding whitespace and trailing newline first. Values are base-10.
func ParseSysfsInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse sysfs int %q: %w", s, err)
	}
	return v, nil
}

const resetStatusPrefix = "get_rsts="

// ParseResetStatus parses `vcgencmd get_rsts` output of the form
// "get_rsts=1020" and returns the reset-status word. The right-hand side is
// parsed with base 0 so a 0x-prefixed value would also be accepted.
func ParseResetStatus(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, resetStatusPrefix) {
		return 0, fmt.Errorf("parse reset status: missing %q prefix in %q", resetStatusPrefix, s)
	}
	rhs := strings.TrimSpace(strings.TrimPrefix(s, resetStatusPrefix))
	v, err := strconv.ParseUint(rhs, 0, 32)
	if err != nil {
		return 0, fmt.Errorf("parse reset status value %q: %w", rhs, err)
	}
	return uint32(v), nil
}

// RingOsc is the decoded form of `vcgencmd read_ring_osc`.
type RingOsc struct {
	Hertz   float64
	Volts   float64
	Celsius float64
}

// ringOscRe matches `vcgencmd read_ring_osc` output, e.g.
// "read_ring_osc(2)=9.368MHz (@0.8749V) (46.6'C)".
//
// Capture groups: 1=frequency value, 2=voltage, 3=temperature. The frequency
// is reported in MHz and is scaled to hertz by the caller.
var ringOscRe = regexp.MustCompile(`^read_ring_osc\(\d+\)=([0-9.]+)MHz\s+\(@([0-9.]+)V\)\s+\(([0-9.]+)'C\)$`)

// ParseRingOsc parses a `vcgencmd read_ring_osc` line, converting the MHz
// frequency to hertz.
func ParseRingOsc(s string) (RingOsc, error) {
	s = strings.TrimSpace(s)
	m := ringOscRe.FindStringSubmatch(s)
	if m == nil {
		return RingOsc{}, fmt.Errorf("parse ring osc: unrecognised input %q", s)
	}

	mhz, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return RingOsc{}, fmt.Errorf("parse ring osc frequency %q: %w", m[1], err)
	}
	volts, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return RingOsc{}, fmt.Errorf("parse ring osc volts %q: %w", m[2], err)
	}
	celsius, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return RingOsc{}, fmt.Errorf("parse ring osc temp %q: %w", m[3], err)
	}

	return RingOsc{
		Hertz:   mhz * 1e6,
		Volts:   volts,
		Celsius: celsius,
	}, nil
}
