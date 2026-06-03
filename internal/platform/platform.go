// Package platform performs Raspberry Pi SoC-family detection from the
// device-tree "compatible" bytes exposed by the kernel, without touching the
// filesystem itself. DetectFamily is pure so it can be exercised with the live
// fixtures captured on the target board.
package platform

import "bytes"

// Family describes the SoC family derived from the device-tree
// "compatible" list.
type Family struct {
	// IsBCM2712 is true when the BCM2712 SoC (Raspberry Pi 5 generation) is
	// present in the compatible list.
	IsBCM2712 bool
	// SoC is the matched SoC compatible token (e.g. "brcm,bcm2712"), or "" if
	// no known SoC was matched.
	SoC string
	// Model is the first (most specific) compatible token, e.g.
	// "raspberrypi,5-model-b".
	Model string
}

// socBCM2712 is the device-tree compatible token for the Raspberry Pi 5
// generation SoC.
const socBCM2712 = "brcm,bcm2712"

// DetectFamily decodes /proc/device-tree/compatible, which is a NUL-separated
// list of NUL-terminated strings. The bytes are split on 0x00 and empty
// tokens are dropped. The first remaining token is taken as Model. If the
// "brcm,bcm2712" token is present, IsBCM2712 is set and SoC records it.
//
// As a convenience for the live test fixtures (which render NUL as a space),
// ASCII spaces are also treated as separators; real device-tree data contains
// no spaces in these tokens, so this is unambiguous.
//
// Empty (or all-empty) input yields the zero Family.
func DetectFamily(compatible []byte) Family {
	var f Family

	// Split on NUL (the real separator) and space (fixture rendering).
	tokens := bytes.FieldsFunc(compatible, func(r rune) bool {
		return r == 0x00 || r == ' '
	})

	for i, tok := range tokens {
		s := string(tok)
		if i == 0 {
			f.Model = s
		}
		if s == socBCM2712 {
			f.IsBCM2712 = true
			f.SoC = socBCM2712
		}
	}

	return f
}
