//go:build pi5_hardware

package mailbox

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestGenCmd_RealHardware exercises the real /dev/vcio ioctl transport end to
// end. It is guarded by the pi5_hardware build tag so CI (which has no /dev/vcio
// and never sets the tag) never compiles or runs it. On a real Pi 5 in the
// 'video' group it proves the live syscall path: build request -> ioctl ->
// parse response.
func TestGenCmd_RealHardware(t *testing.T) {
	if _, err := os.Stat("/dev/vcio"); err != nil {
		t.Skipf("no /dev/vcio, not a Pi 5 / firmware too old: %v", err)
	}

	c, err := Open()
	if err != nil {
		// Most likely the test user is not in the 'video' group.
		t.Skipf("cannot open /dev/vcio (maybe not in 'video' group): %v", err)
	}
	defer c.Close()

	out, err := c.GenCmd("measure_temp")
	if err != nil {
		t.Fatalf("GenCmd(measure_temp): %v", err)
	}

	// Expected shape: temp=NN.N'C  (e.g. "temp=47.7'C")
	if !strings.HasPrefix(out, "temp=") {
		t.Fatalf("result %q does not start with temp=", out)
	}
	rest := strings.TrimPrefix(out, "temp=")
	rest = strings.TrimSuffix(rest, "'C")
	val, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		t.Fatalf("could not parse temperature from %q: %v", out, err)
	}
	if val < 10 || val > 100 {
		t.Fatalf("temperature %.1f out of sane range [10,100] (from %q)", val, out)
	}
	t.Logf("real hardware measure_temp => %q (%.1f C)", out, val)
}
