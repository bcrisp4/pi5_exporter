// Package parse contains pure parsers for the textual output of the
// Raspberry Pi 5 firmware tools (vcgencmd, pmic_read_adc) and the kernel
// sysfs/hwmon files. Everything here is I/O-free: callers pass in the raw
// command output as a string and receive a typed result or an error.
package parse

import (
	"fmt"
	"strconv"
	"strings"
)

// Throttled is the decoded form of `vcgencmd get_throttled`.
//
// The firmware reports a 32-bit bitmask. The low nibble (bits 0..3) describes
// the *current* (live) state and the bits at 16..19 are sticky flags that
// latch if the condition has occurred at any point since boot. The exact bit
// assignments are documented by the Raspberry Pi firmware and reproduced here:
//
//	bit 0  / 16  under-voltage detected
//	bit 1  / 17  arm frequency capped
//	bit 2  / 18  currently throttled
//	bit 3  / 19  soft temperature limit active
type Throttled struct {
	Raw uint32

	// Live bits (0..3): the condition is happening right now.
	UnderVoltageNow  bool
	ArmFreqCappedNow bool
	ThrottledNow     bool
	SoftTempLimitNow bool

	// Sticky-since-boot bits (16..19): the condition has occurred at least
	// once since the last boot.
	UnderVoltageSince  bool
	ArmFreqCappedSince bool
	ThrottledSince     bool
	SoftTempLimitSince bool
}

const throttledPrefix = "throttled="

// ParseThrottled parses a line of the form "throttled=0x<hex>" into a
// Throttled. Input that does not begin with the "throttled=" prefix, or whose
// right-hand side is not a valid 32-bit integer, is rejected with an error.
func ParseThrottled(s string) (Throttled, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, throttledPrefix) {
		return Throttled{}, fmt.Errorf("parse throttled: missing %q prefix in %q", throttledPrefix, s)
	}
	rhs := strings.TrimSpace(strings.TrimPrefix(s, throttledPrefix))

	// Base 0 lets strconv honour the 0x prefix (and would accept decimal too).
	v, err := strconv.ParseUint(rhs, 0, 32)
	if err != nil {
		return Throttled{}, fmt.Errorf("parse throttled value %q: %w", rhs, err)
	}

	raw := uint32(v)
	return Throttled{
		Raw: raw,

		UnderVoltageNow:  raw&(1<<0) != 0,
		ArmFreqCappedNow: raw&(1<<1) != 0,
		ThrottledNow:     raw&(1<<2) != 0,
		SoftTempLimitNow: raw&(1<<3) != 0,

		UnderVoltageSince:  raw&(1<<16) != 0,
		ArmFreqCappedSince: raw&(1<<17) != 0,
		ThrottledSince:     raw&(1<<18) != 0,
		SoftTempLimitSince: raw&(1<<19) != 0,
	}, nil
}
