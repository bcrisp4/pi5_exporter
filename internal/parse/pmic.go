package parse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Rail is a single PMIC power rail as reported by `pmic_read_adc`. The firmware
// emits two lines per rail (a "_A" current line and a "_V" voltage line) which
// are not adjacent; ParsePMIC merges them into one Rail. Volt-only rails such
// as EXT5V and BATT have no current channel, so HasAmps stays false.
type Rail struct {
	Name     string
	Volts    float64
	Amps     float64
	HasVolts bool
	HasAmps  bool
}

// pmicLineRe matches a single pmic_read_adc line, e.g.
//
//	" VDD_CORE_A current(7)=0.56259000A"
//	"   3V3_SYS_V volt(9)=3.32580900V"
//
// Capture groups:
//
//	1: rail name including the _A/_V suffix (e.g. "VDD_CORE_A")
//	2: the unit word "current" or "volt" (channel index is ignored)
//	3: the numeric value
//
// The trailing unit letter (A or V) after the value is consumed but not
// captured.
var pmicLineRe = regexp.MustCompile(`^(\S+)\s+(current|volt)\(\d+\)=([0-9.]+)[AV]$`)

// ParsePMIC parses the full output of `pmic_read_adc`. Each non-blank line is
// matched against pmicLineRe; the _A/_V suffix is stripped to derive the rail
// Name, and the current/voltage lines for the same Name are merged into a
// single Rail. Rails are returned in order of first appearance. Empty input
// returns (nil, nil); any line that fails to match is reported as an error.
func ParsePMIC(s string) ([]Rail, error) {
	var rails []Rail
	// index maps a rail Name to its position in rails so we can merge the
	// non-adjacent current and voltage lines.
	index := make(map[string]int)

	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		m := pmicLineRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("parse pmic: unrecognised line %q", line)
		}

		nameWithSuffix, kind, valStr := m[1], m[2], m[3]

		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return nil, fmt.Errorf("parse pmic value %q on line %q: %w", valStr, line, err)
		}

		// Strip the _A or _V suffix to obtain the canonical rail name.
		name := strings.TrimSuffix(strings.TrimSuffix(nameWithSuffix, "_A"), "_V")

		idx, ok := index[name]
		if !ok {
			idx = len(rails)
			rails = append(rails, Rail{Name: name})
			index[name] = idx
		}

		switch kind {
		case "current":
			rails[idx].Amps = val
			rails[idx].HasAmps = true
		case "volt":
			rails[idx].Volts = val
			rails[idx].HasVolts = true
		}
	}

	return rails, nil
}
