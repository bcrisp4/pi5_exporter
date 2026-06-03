// Package mailbox is the load-bearing transport that talks to the Raspberry Pi
// VideoCore firmware over the /dev/vcio mailbox property interface.
//
// On the Raspberry Pi 5 there is no /dev/vchiq device: the firmware-facing
// "gencmd" channel that vcgencmd uses is routed entirely through the mailbox
// property interface exposed by /dev/vcio. This package reimplements the small
// slice of that protocol needed to issue gencmd strings (e.g. "measure_temp")
// and read back their ASCII results.
//
// Design: everything in this file is pure and unit-testable except the single
// ioctl syscall in Open's real transport (see open.go). The ioctl is injected
// as an IoctlFunc seam so the protocol marshalling can be exercised without
// hardware.
//
// Protocol provenance (verified live on a Pi 5 this session via strace + the
// raspberrypi/utils vcgencmd.c source):
//
//   - ioctl request = _IOWR('d'=100, 0, char*) = 0xC0086400 on arm64.
//     Computed as dir(3)<<30 | size(8)<<16 | type(100)<<8 | nr(0); size 8 is
//     sizeof(char*) on a 64-bit kernel. (see open.go)
//   - The gencmd tag GET_GENCMD_RESULT = 0x00030080. This tag is NOT in the
//     mainline kernel mailbox header because the kernel itself never issues
//     gencmd; it is defined by raspberrypi/utils vcgencmd.c. Confirmed by the
//     tag word observed in strace.
//   - MAX_STRING = 4096 (the value-buffer size, from vcgencmd.c).
package mailbox

import (
	"encoding/binary"
	"fmt"
)

const (
	// tagGetGenCmdResult is the mailbox property tag for the gencmd channel.
	// Provenance: raspberrypi/utils vcgencmd.c (GET_GENCMD_RESULT). The mainline
	// kernel header does not define this because the kernel never issues gencmd.
	tagGetGenCmdResult = 0x00030080

	// maxStringBytes is the value-buffer size in bytes (MAX_STRING in
	// vcgencmd.c). Both the request command and the response result share this
	// region.
	maxStringBytes = 4096

	// valueWordOffset is the index of the first word of the value region in the
	// request/response buffer. The header occupies words [0..5]:
	//   [0] total buffer size in bytes (filled in last)
	//   [1] request/response code (0 = process request)
	//   [2] tag id (tagGetGenCmdResult)
	//   [3] value buffer size in bytes (maxStringBytes)
	//   [4] tag request/response code (0 on request)
	//   [5] firmware return code slot (0 = ok on response)
	valueWordOffset = 6

	// valueWords is the value region size in 32-bit words (4096 bytes).
	valueWords = maxStringBytes / 4 // 1024

	// totalWords is the full buffer size in 32-bit words:
	//   6 header words + 1024 value words + 1 end-tag word = 1031.
	totalWords = valueWordOffset + valueWords + 1 // 1031
)

// BuildGenCmd builds the 1031-word mailbox property request buffer for the given
// gencmd command string. The buffer layout (little-endian uint32 words) is:
//
//	[0]      = total buffer size in bytes (1031*4), set last
//	[1]      = 0 (process-request code)
//	[2]      = 0x00030080 (GET_GENCMD_RESULT tag)
//	[3]      = 4096 (value buffer size in bytes)
//	[4]      = 0 (tag request code)
//	[5]      = 0 (firmware return-code slot; filled by firmware on response)
//	[6..]    = the command string, NUL-terminated, occupying the 4096-byte
//	           (1024-word) value region
//	[1030]   = 0 (mailbox end tag)
//
// BuildGenCmd is pure: it allocates and returns a fresh buffer and reads no
// global or hardware state.
func BuildGenCmd(cmd string) []uint32 {
	buf := make([]uint32, totalWords)
	buf[1] = 0                  // process request
	buf[2] = tagGetGenCmdResult // tag id
	buf[3] = maxStringBytes     // value buffer size in bytes
	buf[4] = 0                  // tag request code
	buf[5] = 0                  // firmware return code slot (response only)

	// Pack the command string into the value region, NUL-terminated. make()
	// zeroes the buffer, so any bytes past the string (including the
	// terminator) are already 0.
	packString(buf, valueWordOffset, cmd)

	// The end-tag word [1030] is already 0.

	// Total buffer size in bytes is filled in last, mirroring how the mailbox
	// property interface request is conventionally assembled.
	buf[0] = totalWords * 4
	return buf
}

// ParseGenCmdResult interprets a mailbox response buffer that was produced by
// the firmware in reply to a BuildGenCmd request.
//
// If buf[5] (the firmware return code) is non-zero, the command failed and a
// *GenCmdError is returned carrying the code and the ASCII body that the
// firmware wrote into the value region (e.g. error=1 error_msg="...").
//
// Otherwise the value region starting at word 6 is read as ASCII up to the
// first NUL byte and returned as the result string.
//
// ParseGenCmdResult is pure.
func ParseGenCmdResult(buf []uint32) (string, error) {
	// Defensive bound: buf[5] is the return code and the value region starts at
	// word 6. A well-formed response is always 1031 words, but this is an
	// exported function fed by an injectable transport, so guard a short buffer
	// rather than panicking the scheduler goroutine.
	if len(buf) <= valueWordOffset {
		return "", fmt.Errorf("parse gencmd: response buffer too short (%d words)", len(buf))
	}
	body := unpackString(buf, valueWordOffset)
	if buf[5] != 0 {
		return "", &GenCmdError{Code: buf[5], Body: body}
	}
	return body, nil
}

// GenCmdError reports a non-zero firmware return code from a gencmd request.
// Code is the raw firmware return code (e.g. 0xffffffff for an unknown
// command); Body is the ASCII payload the firmware wrote into the value region.
type GenCmdError struct {
	Code uint32
	Body string
}

// Error implements the error interface.
func (e *GenCmdError) Error() string {
	return fmt.Sprintf("gencmd firmware error 0x%x: %q", e.Code, e.Body)
}

// packString writes s into buf as NUL-terminated little-endian bytes, starting
// at the given word index. The caller must ensure buf has room for the string
// plus its NUL terminator; callers in this package always pass the 1024-word
// value region. buf words at/after the write are assumed to be zero (so the NUL
// terminator and any trailing slack are already 0), matching make()'s zeroing;
// packString therefore only writes the non-zero string bytes.
//
// packString is pure (operates only on its arguments).
func packString(buf []uint32, wordIdx int, s string) {
	for i := 0; i < len(s); i++ {
		w := wordIdx + i/4     // which word this byte lands in
		shift := uint(i%4) * 8 // little-endian byte position within the word
		buf[w] |= uint32(s[i]) << shift
	}
	// The terminating NUL and any remaining bytes are left as the existing
	// (zero) buffer contents.
}

// unpackString reads NUL-terminated ASCII from buf starting at the given word
// index and returns it as a string (excluding the NUL). It reads at most the
// value region; reading stops at the first NUL byte or the end of the buffer.
//
// unpackString is pure.
func unpackString(buf []uint32, wordIdx int) string {
	// Reconstruct the value-region bytes, little-endian, then cut at the first
	// NUL. We bound the scan to the value words to avoid reading the end tag.
	end := wordIdx + valueWords
	if end > len(buf) {
		end = len(buf)
	}
	raw := make([]byte, 0, (end-wordIdx)*4)
	var word [4]byte
	for w := wordIdx; w < end; w++ {
		binary.LittleEndian.PutUint32(word[:], buf[w])
		for b := 0; b < 4; b++ {
			if word[b] == 0 {
				return string(raw)
			}
			raw = append(raw, word[b])
		}
	}
	return string(raw)
}
