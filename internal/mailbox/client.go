package mailbox

import "fmt"

// IoctlFunc is the injected seam for the impure ioctl call. It is handed the
// request buffer (as built by BuildGenCmd) and must, on success, mutate that
// same buffer in place with the firmware's response (notably buf[5] and the
// value region). A non-nil return indicates the syscall itself failed.
type IoctlFunc func(buf []uint32) error

// Client issues gencmd requests to the VideoCore firmware. The zero value is
// not usable; construct one with Open (real hardware) or NewClient (tests).
type Client struct {
	// ioctl performs the transport. For a real client this wraps the /dev/vcio
	// ioctl; for tests it is a fake.
	ioctl IoctlFunc
	// fd is the open /dev/vcio file descriptor, or -1 for an injected client
	// that owns no descriptor.
	fd int
}

// NewClient constructs a Client around an injected IoctlFunc. The returned
// client owns no file descriptor (fd = -1), so Close is a no-op. This is the
// entry point for tests and for callers that wish to supply their own
// transport.
func NewClient(ioctl IoctlFunc) *Client {
	return &Client{ioctl: ioctl, fd: -1}
}

// GenCmd issues a single gencmd command (e.g. "measure_temp") and returns the
// firmware's ASCII result with any trailing NUL stripped.
//
// It builds the request with BuildGenCmd, performs the (impure) ioctl, then
// parses the mutated buffer with ParseGenCmdResult. A transport failure is
// wrapped with the command for context; a non-zero firmware return code surfaces
// as a *GenCmdError from ParseGenCmdResult.
func (c *Client) GenCmd(cmd string) (string, error) {
	buf := BuildGenCmd(cmd)
	if err := c.ioctl(buf); err != nil {
		return "", fmt.Errorf("mailbox ioctl for %q: %w", cmd, err)
	}
	return ParseGenCmdResult(buf)
}
