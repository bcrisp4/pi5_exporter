package mailbox

import (
	"errors"
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/unix"
)

// devVCIO is the mailbox property-interface character device on the Raspberry
// Pi. On the Pi 5 this is the only path to the firmware gencmd channel (there
// is no /dev/vchiq).
const devVCIO = "/dev/vcio"

// ioctlGenCmd is the ioctl request number for a mailbox property call.
//
// Provenance: _IOWR('d'=100, 0, char*) on a 64-bit (arm64) kernel.
//
//	dir(3) << 30 | size(8) << 16 | type(100) << 8 | nr(0)
//	= 0xC0086400
//
// where dir 3 = _IOC_READ|_IOC_WRITE, size 8 = sizeof(char*) on arm64, type
// 'd' = 100, nr = 0. Verified live this session via strace and recomputation.
const ioctlGenCmd = 0xC0086400

// Open opens /dev/vcio and returns a Client whose ioctl seam performs the real
// mailbox property-interface syscall.
//
// Errors are classified to give an actionable message:
//   - permission denied (EACCES / fs.ErrPermission): the caller is likely not
//     in the 'video' group, which owns /dev/vcio.
//   - not found (ENOENT / fs.ErrNotExist): /dev/vcio is absent, meaning this is
//     not a Pi 5 (or the firmware/kernel is too old to expose it).
func Open() (*Client, error) {
	fd, err := unix.Open(devVCIO, unix.O_RDWR, 0)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrPermission) || errors.Is(err, unix.EACCES):
			return nil, fmt.Errorf("open %s: permission denied (is this user in the 'video' group?): %w", devVCIO, err)
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOENT):
			return nil, fmt.Errorf("open %s: no /dev/vcio (not a Pi 5 / firmware too old): %w", devVCIO, err)
		default:
			return nil, fmt.Errorf("open %s: %w", devVCIO, err)
		}
	}

	c := &Client{fd: fd}
	c.ioctl = c.realIoctl
	return c, nil
}

// vcioIoctl performs the mailbox property ioctl against the open /dev/vcio
// descriptor. The firmware reads and writes the buffer in place. This is the
// single impure operation in the package.
func vcioIoctl(fd int, buf []uint32) error {
	// The kernel ioctl handler treats the third argument as a pointer to the
	// property buffer. &buf[0] stays valid for the duration of the syscall
	// because buf is reachable on the caller's stack/heap; unsafe.Pointer is
	// required to hand the Go slice's backing array address to the syscall.
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		uintptr(ioctlGenCmd),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// realIoctl is the method form bound to a Client's fd, used as its IoctlFunc.
func (c *Client) realIoctl(buf []uint32) error {
	return vcioIoctl(c.fd, buf)
}

// Close releases the underlying /dev/vcio descriptor if this client owns one.
// Clients constructed with NewClient (fd = -1) own no descriptor, so Close is a
// no-op for them.
func (c *Client) Close() error {
	if c.fd >= 0 {
		return unix.Close(c.fd)
	}
	return nil
}
