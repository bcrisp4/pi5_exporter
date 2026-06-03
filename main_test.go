package main

import (
	"errors"
	"log/slog"
	"testing"
)

type fakeFirmwareClient struct{}

func (fakeFirmwareClient) GenCmd(string) (string, error) { return "", nil }
func (fakeFirmwareClient) Close() error                  { return nil }

func TestResolveFirmware(t *testing.T) {
	const (
		bcm2712 = "raspberrypi,5-model-b\x00brcm,bcm2712\x00"
		bcm2711 = "raspberrypi,4-model-b\x00brcm,bcm2711\x00"
	)
	readErr := func() ([]byte, error) { return nil, errors.New("no such file") }
	logger := slog.New(slog.DiscardHandler)

	tests := []struct {
		name       string
		read       func() ([]byte, error)
		openOK     bool
		wantAvail  bool
		wantOpened bool // whether the mailbox open was attempted
	}{
		{"pi5: device-tree + mailbox ok", func() ([]byte, error) { return []byte(bcm2712), nil }, true, true, true},
		{"non-bcm2712 short-circuits; mailbox never tried", func() ([]byte, error) { return []byte(bcm2711), nil }, true, false, false},
		{"unreadable device-tree falls through to mailbox (container)", readErr, true, true, true},
		{"unreadable device-tree + no mailbox", readErr, false, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opened := false
			open := func() (firmwareClient, error) {
				opened = true
				if !tc.openOK {
					return nil, errors.New("ENOENT")
				}
				return fakeFirmwareClient{}, nil
			}
			client, avail := resolveFirmware(tc.read, open, logger)
			if avail != tc.wantAvail {
				t.Fatalf("available = %v, want %v", avail, tc.wantAvail)
			}
			if opened != tc.wantOpened {
				t.Errorf("mailbox open attempted = %v, want %v", opened, tc.wantOpened)
			}
			if avail != (client != nil) {
				t.Errorf("client non-nil = %v but available = %v", client != nil, avail)
			}
		})
	}
}
