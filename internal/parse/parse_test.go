package parse

import (
	"math"
	"testing"
)

// floatEq compares two floats with a small tolerance suitable for the
// fixed-precision decimal strings emitted by vcgencmd / pmic_read_adc.
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestParseThrottled(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Throttled
		wantErr bool
	}{
		{
			name: "none set",
			in:   "throttled=0x0",
			want: Throttled{Raw: 0x0},
		},
		{
			name: "synthetic UV+throttled live+since",
			in:   "throttled=0x50005",
			want: Throttled{
				Raw:               0x50005,
				UnderVoltageNow:   true,
				ThrottledNow:      true,
				UnderVoltageSince: true,
				ThrottledSince:    true,
			},
		},
		{
			name: "all live bits",
			in:   "throttled=0xf",
			want: Throttled{
				Raw:              0xf,
				UnderVoltageNow:  true,
				ArmFreqCappedNow: true,
				ThrottledNow:     true,
				SoftTempLimitNow: true,
			},
		},
		{
			name: "all since bits",
			in:   "throttled=0xf0000",
			want: Throttled{
				Raw:                0xf0000,
				UnderVoltageSince:  true,
				ArmFreqCappedSince: true,
				ThrottledSince:     true,
				SoftTempLimitSince: true,
			},
		},
		{
			name:    "missing prefix",
			in:      "0x50005",
			wantErr: true,
		},
		{
			name:    "garbage rhs",
			in:      "throttled=notahex",
			wantErr: true,
		},
		{
			name:    "empty",
			in:      "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseThrottled(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseThrottled(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("ParseThrottled(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// pmicFixture is the verbatim 26-line pmic_read_adc output captured live on
// this board. Leading spaces, non-sequential channel indices and the
// volt-only EXT5V/BATT rails are all preserved exactly.
const pmicFixture = ` 3V7_WL_SW_A current(0)=0.00195186A
   3V3_SYS_A current(1)=0.12687090A
   1V8_SYS_A current(2)=0.18835450A
  DDR_VDD2_A current(3)=0.02147046A
  DDR_VDDQ_A current(4)=0.00000000A
   1V1_SYS_A current(5)=0.17957110A
    0V8_SW_A current(6)=0.25959740A
  VDD_CORE_A current(7)=0.56259000A
   3V3_DAC_A current(17)=0.00000000A
   3V3_ADC_A current(18)=0.00000000A
   0V8_AON_A current(16)=0.00293040A
      HDMI_A current(22)=0.02551890A
 3V7_WL_SW_V volt(8)=3.70720000V
   3V3_SYS_V volt(9)=3.32580900V
   1V8_SYS_V volt(10)=1.80415000V
  DDR_VDD2_V volt(11)=1.11135400V
  DDR_VDDQ_V volt(12)=0.60329610V
   1V1_SYS_V volt(13)=1.11282000V
    0V8_SW_V volt(14)=0.80512740V
  VDD_CORE_V volt(15)=0.76798460V
   3V3_DAC_V volt(20)=3.32142500V
   3V3_ADC_V volt(21)=3.32966700V
   0V8_AON_V volt(19)=0.80175740V
      HDMI_V volt(23)=5.11746000V
     EXT5V_V volt(24)=5.14158000V
      BATT_V volt(25)=3.28033900V`

func findRail(rails []Rail, name string) (Rail, bool) {
	for _, r := range rails {
		if r.Name == name {
			return r, true
		}
	}
	return Rail{}, false
}

func TestParsePMIC(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		got, err := ParsePMIC("")
		if err != nil {
			t.Fatalf("ParsePMIC(\"\") err=%v want nil", err)
		}
		if got != nil {
			t.Errorf("ParsePMIC(\"\") = %v, want nil", got)
		}
	})

	t.Run("whitespace only", func(t *testing.T) {
		got, err := ParsePMIC("\n   \n\t\n")
		if err != nil {
			t.Fatalf("err=%v want nil", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("garbage line errors", func(t *testing.T) {
		if _, err := ParsePMIC("this is not a valid line"); err == nil {
			t.Errorf("ParsePMIC(garbage) err=nil, want error")
		}
	})

	t.Run("full fixture", func(t *testing.T) {
		rails, err := ParsePMIC(pmicFixture)
		if err != nil {
			t.Fatalf("ParsePMIC(fixture) err=%v", err)
		}
		if len(rails) != 14 {
			t.Fatalf("got %d rails, want 14", len(rails))
		}
		if rails[0].Name != "3V7_WL_SW" {
			t.Errorf("first rail Name=%q, want %q", rails[0].Name, "3V7_WL_SW")
		}

		// VDD_CORE has both current and voltage merged.
		vc, ok := findRail(rails, "VDD_CORE")
		if !ok {
			t.Fatalf("VDD_CORE rail not found")
		}
		if !vc.HasAmps || !vc.HasVolts {
			t.Errorf("VDD_CORE HasAmps=%v HasVolts=%v, want both true", vc.HasAmps, vc.HasVolts)
		}
		if !floatEq(vc.Amps, 0.56259) {
			t.Errorf("VDD_CORE Amps=%v, want 0.56259", vc.Amps)
		}
		if !floatEq(vc.Volts, 0.76798460) {
			t.Errorf("VDD_CORE Volts=%v, want 0.76798460", vc.Volts)
		}

		// EXT5V is volt-only.
		ext, ok := findRail(rails, "EXT5V")
		if !ok {
			t.Fatalf("EXT5V rail not found")
		}
		if ext.HasAmps {
			t.Errorf("EXT5V HasAmps=true, want false")
		}
		if !ext.HasVolts {
			t.Errorf("EXT5V HasVolts=false, want true")
		}
		if !floatEq(ext.Volts, 5.14158) {
			t.Errorf("EXT5V Volts=%v, want 5.14158", ext.Volts)
		}

		// BATT is volt-only too.
		batt, ok := findRail(rails, "BATT")
		if !ok {
			t.Fatalf("BATT rail not found")
		}
		if batt.HasAmps {
			t.Errorf("BATT HasAmps=true, want false")
		}
		if !floatEq(batt.Volts, 3.28033900) {
			t.Errorf("BATT Volts=%v, want 3.28033900", batt.Volts)
		}

		// Ordering: first appearance order, current lines come first.
		wantOrder := []string{
			"3V7_WL_SW", "3V3_SYS", "1V8_SYS", "DDR_VDD2", "DDR_VDDQ",
			"1V1_SYS", "0V8_SW", "VDD_CORE", "3V3_DAC", "3V3_ADC",
			"0V8_AON", "HDMI", "EXT5V", "BATT",
		}
		for i, name := range wantOrder {
			if rails[i].Name != name {
				t.Errorf("rails[%d].Name=%q, want %q", i, rails[i].Name, name)
			}
		}
	})
}

func TestParseVolts(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{name: "core", in: "volt=0.8749V", want: 0.8749},
		{name: "sdram_c", in: "volt=0.6000V", want: 0.6000},
		{name: "sdram_p", in: "volt=1.1000V", want: 1.1000},
		{name: "bad argument", in: "bad argument", wantErr: true},
		{name: "cmd not registered", in: `error=1 error_msg="Command not registered"`, wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVolts(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseVolts(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !floatEq(got, tt.want) {
				t.Errorf("ParseVolts(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseClockHertz(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{name: "arm", in: "frequency(0)=1600020224", want: 1.600020224e9},
		{name: "h264 zero", in: "frequency(0)=0", want: 0},
		{name: "bad argument", in: "bad argument", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseClockHertz(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseClockHertz(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !floatEq(got, tt.want) {
				t.Errorf("ParseClockHertz(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseTempCelsius(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{name: "soc", in: "temp=46.6'C", want: 46.6},
		{name: "pmic", in: "temp=43.7'C", want: 43.7},
		{name: "bad argument", in: "bad argument", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTempCelsius(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTempCelsius(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !floatEq(got, tt.want) {
				t.Errorf("ParseTempCelsius(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

const versionFixture = `2026/05/11 12:20:02
Copyright (c) 2012 Broadcom
version 66f33f7e (release) (embedded)`

func TestParseVersion(t *testing.T) {
	t.Run("full fixture", func(t *testing.T) {
		got, err := ParseVersion(versionFixture)
		if err != nil {
			t.Fatalf("ParseVersion err=%v", err)
		}
		if got.Hash != "66f33f7e" {
			t.Errorf("Hash=%q, want %q", got.Hash, "66f33f7e")
		}
		if got.Variant != "release" {
			t.Errorf("Variant=%q, want %q", got.Variant, "release")
		}
		if got.Build != "embedded" {
			t.Errorf("Build=%q, want %q", got.Build, "embedded")
		}
		if got.FirmwareDate != "2026/05/11 12:20:02" {
			t.Errorf("FirmwareDate=%q, want %q", got.FirmwareDate, "2026/05/11 12:20:02")
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, err := ParseVersion(""); err == nil {
			t.Errorf("ParseVersion(\"\") err=nil, want error")
		}
	})

	t.Run("malformed last line", func(t *testing.T) {
		if _, err := ParseVersion("not a version line at all"); err == nil {
			t.Errorf("expected error for malformed last line")
		}
	})
}

func TestParseSysfsInt(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "trailing newline", in: "3282048\n", want: 3282048},
		{name: "plain", in: "3282048", want: 3282048},
		{name: "leading/trailing whitespace", in: "  42 \n", want: 42},
		{name: "negative", in: "-5\n", want: -5},
		{name: "garbage", in: "abc", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSysfsInt(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSysfsInt(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("ParseSysfsInt(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseResetStatus(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    uint32
		wantErr bool
	}{
		{name: "rsts", in: "get_rsts=1020", want: 1020},
		{name: "hex", in: "get_rsts=0x10", want: 0x10},
		{name: "missing prefix", in: "1020", wantErr: true},
		{name: "garbage rhs", in: "get_rsts=xyz", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResetStatus(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseResetStatus(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("ParseResetStatus(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRingOsc(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    RingOsc
		wantErr bool
	}{
		{
			name: "fixture",
			in:   "read_ring_osc(2)=9.368MHz (@0.8749V) (46.6'C)",
			want: RingOsc{Hertz: 9.368e6, Volts: 0.8749, Celsius: 46.6},
		},
		{name: "bad argument", in: "bad argument", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRingOsc(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseRingOsc(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !floatEq(got.Hertz, tt.want.Hertz) {
				t.Errorf("Hertz=%v, want %v", got.Hertz, tt.want.Hertz)
			}
			if !floatEq(got.Volts, tt.want.Volts) {
				t.Errorf("Volts=%v, want %v", got.Volts, tt.want.Volts)
			}
			if !floatEq(got.Celsius, tt.want.Celsius) {
				t.Errorf("Celsius=%v, want %v", got.Celsius, tt.want.Celsius)
			}
		})
	}
}
