package platform

import "testing"

func TestDetectFamily(t *testing.T) {
	tests := []struct {
		name       string
		compatible []byte
		want       Family
	}{
		{
			// VERBATIM fixture captured live: /proc/device-tree/compatible
			// on a Raspberry Pi 5 Model B. Real file uses NUL separators; the
			// fixture renders them as spaces, which Split must treat the same.
			name:       "pi5_model_b",
			compatible: []byte("raspberrypi,5-model-b brcm,bcm2712 "),
			want: Family{
				IsBCM2712: true,
				SoC:       "brcm,bcm2712",
				Model:     "raspberrypi,5-model-b",
			},
		},
		{
			name:       "pi500",
			compatible: []byte("raspberrypi,500 brcm,bcm2712 "),
			want: Family{
				IsBCM2712: true,
				SoC:       "brcm,bcm2712",
				Model:     "raspberrypi,500",
			},
		},
		{
			name:       "cm5",
			compatible: []byte("raspberrypi,5-compute-module brcm,bcm2712 "),
			want: Family{
				IsBCM2712: true,
				SoC:       "brcm,bcm2712",
				Model:     "raspberrypi,5-compute-module",
			},
		},
		{
			// Negative: Pi 4 uses BCM2711, must not be detected as BCM2712.
			name:       "pi4_model_b_negative",
			compatible: []byte("raspberrypi,4-model-b brcm,bcm2711 "),
			want: Family{
				IsBCM2712: false,
				SoC:       "",
				Model:     "raspberrypi,4-model-b",
			},
		},
		{
			name:       "nul_separated_real_layout",
			compatible: []byte("raspberrypi,5-model-b\x00brcm,bcm2712\x00"),
			want: Family{
				IsBCM2712: true,
				SoC:       "brcm,bcm2712",
				Model:     "raspberrypi,5-model-b",
			},
		},
		{
			name:       "empty",
			compatible: []byte(""),
			want:       Family{},
		},
		{
			name:       "nil",
			compatible: nil,
			want:       Family{},
		},
		{
			name:       "only_nuls",
			compatible: []byte("\x00\x00\x00"),
			want:       Family{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFamily(tt.compatible)
			if got != tt.want {
				t.Errorf("DetectFamily(%q) = %+v, want %+v", tt.compatible, got, tt.want)
			}
		})
	}
}
