package mailbox

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// packStringInto writes s as NUL-terminated little-endian bytes starting at the
// given word index of a freshly-zeroed buffer. It is a test helper that mirrors
// how the firmware would lay out an ASCII result in the value region, so we can
// craft canned responses without depending on the production packing code.
func packStringInto(t *testing.T, buf []uint32, wordIdx int, s string) {
	t.Helper()
	raw := make([]byte, len(buf)*4)
	for i, w := range buf {
		binary.LittleEndian.PutUint32(raw[i*4:], w)
	}
	copy(raw[wordIdx*4:], []byte(s))
	// Ensure NUL terminator (copy of []byte(s) does not add one).
	raw[wordIdx*4+len(s)] = 0
	for i := range buf {
		buf[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
}

func TestBuildGenCmd(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "measure_temp", cmd: "measure_temp"},
		{name: "get_throttled", cmd: "get_throttled"},
		{name: "empty", cmd: ""},
		{name: "with_args", cmd: "get_config arm_freq"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := BuildGenCmd(tc.cmd)

			if got := len(buf); got != 1031 {
				t.Fatalf("len(buf) = %d, want 1031", got)
			}
			if got := buf[0]; got != 1031*4 {
				t.Errorf("buf[0] (total bytes) = %d, want %d", got, 1031*4)
			}
			if got := buf[1]; got != 0 {
				t.Errorf("buf[1] (request code) = %#x, want 0", got)
			}
			if got := buf[2]; got != 0x00030080 {
				t.Errorf("buf[2] (tag) = %#x, want 0x00030080", got)
			}
			if got := buf[3]; got != 4096 {
				t.Errorf("buf[3] (value buffer size) = %d, want 4096", got)
			}
			if got := buf[4]; got != 0 {
				t.Errorf("buf[4] (tag request code) = %#x, want 0", got)
			}
			if got := buf[5]; got != 0 {
				t.Errorf("buf[5] (retcode slot) = %#x, want 0", got)
			}
			if got := buf[len(buf)-1]; got != 0 {
				t.Errorf("last word (end tag) = %#x, want 0", got)
			}

			// The command string must be packed LE starting at word 6 and
			// NUL-terminated within the value region.
			raw := make([]byte, len(buf)*4)
			for i, w := range buf {
				binary.LittleEndian.PutUint32(raw[i*4:], w)
			}
			region := raw[6*4:]
			nul := -1
			for i := 0; i < len(region); i++ {
				if region[i] == 0 {
					nul = i
					break
				}
			}
			if nul == -1 {
				t.Fatalf("command string not NUL-terminated in value region")
			}
			if got := string(region[:nul]); got != tc.cmd {
				t.Errorf("packed command = %q, want %q", got, tc.cmd)
			}
		})
	}
}

func TestParseGenCmdResult(t *testing.T) {
	t.Run("ok with trailing space trimmed at NUL", func(t *testing.T) {
		// Verbatim fixture captured live on this board (vcgencmd measure_temp).
		// The firmware NUL-terminates; here we deliberately include a trailing
		// space before the NUL to prove we read up to the first NUL, not that
		// we trim whitespace. So the expected output keeps no trailing space
		// because the NUL is placed right after the space-bearing string... see
		// the explicit construction below.
		buf := BuildGenCmd("measure_temp")
		buf[5] = 0
		packStringInto(t, buf, 6, "temp=46.6'C")
		got, err := ParseGenCmdResult(buf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "temp=46.6'C" {
			t.Errorf("got %q, want %q", got, "temp=46.6'C")
		}
	})

	t.Run("ok reads up to first NUL", func(t *testing.T) {
		// "temp=46.6'C " then NUL: parser must stop at NUL, returning the bytes
		// before it (including the trailing space).
		buf := BuildGenCmd("measure_temp")
		buf[5] = 0
		// Manually place "temp=46.6'C \x00garbage" to confirm NUL handling.
		raw := make([]byte, len(buf)*4)
		for i, w := range buf {
			binary.LittleEndian.PutUint32(raw[i*4:], w)
		}
		copy(raw[6*4:], []byte("temp=46.6'C \x00garbage"))
		for i := range buf {
			buf[i] = binary.LittleEndian.Uint32(raw[i*4:])
		}
		got, err := ParseGenCmdResult(buf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "temp=46.6'C " {
			t.Errorf("got %q, want %q", got, "temp=46.6'C ")
		}
	})

	t.Run("firmware error", func(t *testing.T) {
		// Verbatim fixture captured live: vcgencmd of an unknown command.
		body := `error=1 error_msg="Command not registered"`
		buf := BuildGenCmd("this_is_not_a_command")
		buf[5] = 0xffffffff
		packStringInto(t, buf, 6, body)

		_, err := ParseGenCmdResult(buf)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var ge *GenCmdError
		if !errors.As(err, &ge) {
			t.Fatalf("error is not *GenCmdError: %T (%v)", err, err)
		}
		if ge.Code != 0xffffffff {
			t.Errorf("Code = %#x, want 0xffffffff", ge.Code)
		}
		if ge.Body != body {
			t.Errorf("Body = %q, want %q", ge.Body, body)
		}
		// Error() should mention the code and body.
		msg := ge.Error()
		if !strings.Contains(msg, "ffffffff") {
			t.Errorf("Error() = %q, want it to contain the code", msg)
		}
		if !strings.Contains(msg, "Command not registered") {
			t.Errorf("Error() = %q, want it to contain the body", msg)
		}
	})
}

func TestGenCmdError_Error(t *testing.T) {
	e := &GenCmdError{Code: 0xffffffff, Body: `error=1 error_msg="x"`}
	got := e.Error()
	want := `gencmd firmware error 0xffffffff: "error=1 error_msg=\"x\""`
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestClientGenCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Fake firmware: validate the inbound request, then write a canned
		// response. This is the round-trip helper (~15 lines), not an emulator.
		fake := func(buf []uint32) error {
			if len(buf) != 1031 {
				t.Errorf("ioctl got len %d, want 1031", len(buf))
			}
			if buf[2] != 0x00030080 {
				t.Errorf("ioctl got tag %#x, want 0x00030080", buf[2])
			}
			if buf[3] != 4096 {
				t.Errorf("ioctl got value size %d, want 4096", buf[3])
			}
			// Confirm the command we sent round-trips through the buffer.
			raw := make([]byte, len(buf)*4)
			for i, w := range buf {
				binary.LittleEndian.PutUint32(raw[i*4:], w)
			}
			region := raw[6*4:]
			n := 0
			for n < len(region) && region[n] != 0 {
				n++
			}
			if got := string(region[:n]); got != "get_throttled" {
				t.Errorf("ioctl got cmd %q, want %q", got, "get_throttled")
			}
			// Write canned response.
			buf[5] = 0
			packStringInto(t, buf, 6, "throttled=0x0")
			return nil
		}
		c := NewClient(fake)
		out, err := c.GenCmd("get_throttled")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "throttled=0x0" {
			t.Errorf("GenCmd = %q, want %q", out, "throttled=0x0")
		}
	})

	t.Run("ioctl error wrapped", func(t *testing.T) {
		sentinel := errors.New("boom")
		fake := func(buf []uint32) error { return sentinel }
		c := NewClient(fake)
		_, err := c.GenCmd("measure_temp")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("error %v does not wrap sentinel", err)
		}
		if !strings.Contains(err.Error(), "measure_temp") {
			t.Errorf("error %q does not mention the command", err.Error())
		}
	})

	t.Run("firmware error propagated", func(t *testing.T) {
		body := `error=1 error_msg="Command not registered"`
		fake := func(buf []uint32) error {
			buf[5] = 0xffffffff
			packStringInto(t, buf, 6, body)
			return nil
		}
		c := NewClient(fake)
		_, err := c.GenCmd("bogus")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		var ge *GenCmdError
		if !errors.As(err, &ge) {
			t.Fatalf("error is not *GenCmdError: %T (%v)", err, err)
		}
		if ge.Code != 0xffffffff {
			t.Errorf("Code = %#x, want 0xffffffff", ge.Code)
		}
		if ge.Body != body {
			t.Errorf("Body = %q, want %q", ge.Body, body)
		}
	})
}

func TestNewClientCloseNoFD(t *testing.T) {
	// A client built via NewClient has fd=-1, so Close must be a no-op.
	c := NewClient(func(buf []uint32) error { return nil })
	if err := c.Close(); err != nil {
		t.Errorf("Close() on injected client = %v, want nil", err)
	}
}

// Test the pure packing helpers directly.
func TestPackUnpackString(t *testing.T) {
	tests := []struct {
		name string
		s    string
	}{
		{name: "simple", s: "temp=46.6'C"},
		{name: "empty", s: ""},
		{name: "with quotes", s: `error=1 error_msg="Command not registered"`},
		{name: "exactly one word", s: "abcd"},
		{name: "not word aligned", s: "abcde"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			words := make([]uint32, 256)
			packString(words, 6, tc.s)
			got := unpackString(words, 6)
			if got != tc.s {
				t.Errorf("round-trip = %q, want %q", got, tc.s)
			}
		})
	}
}

// TestParseGenCmdResultShortBuffer ensures a malformed/short response buffer
// returns an error rather than panicking the caller (the scheduler goroutine).
func TestParseGenCmdResultShortBuffer(t *testing.T) {
	for _, n := range []int{0, 1, 5, 6} {
		buf := make([]uint32, n)
		if _, err := ParseGenCmdResult(buf); err == nil {
			t.Errorf("ParseGenCmdResult(len=%d) error = nil, want error", n)
		}
	}
}
