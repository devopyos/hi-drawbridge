//go:build linux

package probe

import (
	"testing"

	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func TestDecodeFeatureFrame(t *testing.T) {
	p := profile.ProfileSpec{
		ID:                "p1",
		Name:              "Test",
		QueryLength:       6,
		QueryReportID:     0x01,
		ExpectedSignature: []byte{0xAA, 0xBB},
		BatteryOffset:     2,
		StatusOffset:      3,
		DeviceType:        "mouse",
		IconName:          "input-mouse",
	}

	candidate := model.HidCandidate{
		StableDeviceID: "dev1",
		Transport:      model.TransportUSBDirect,
		Path:           "/dev/hidraw0",
	}

	frame := []byte{0x01, 0x00, 50, 5, 0xAA, 0xBB}
	reading := decodeFeatureFrame(frame, candidate, p)
	if reading == nil {
		t.Fatal("expected reading")
	}

	if reading.Percentage != 50 {
		t.Fatalf("expected percentage 50, got %d", reading.Percentage)
	}
	if reading.Status == nil || *reading.Status != 5 {
		t.Fatalf("expected status 5, got %v", reading.Status)
	}
	if reading.Source != model.FrameSourceFeature {
		t.Fatalf("expected feature source, got %s", reading.Source)
	}

	badFrame := []byte{0x02, 0x00, 50, 5, 0xAA, 0xBB}
	if decodeFeatureFrame(badFrame, candidate, p) != nil {
		t.Fatal("expected nil reading for wrong report id")
	}
}

func TestDecodeInterruptFrame(t *testing.T) {
	p := profile.ProfileSpec{
		ID:                       "p1",
		Name:                     "Test",
		FallbackInputReportID:    0x10,
		FallbackInputCmd:         0x20,
		FallbackInputLength:      5,
		FallbackBatteryOffset:    2,
		FallbackBatteryBucketMax: 4,
		DeviceType:               "mouse",
		IconName:                 "input-mouse",
	}

	candidate := model.HidCandidate{
		StableDeviceID: "dev1",
		Transport:      model.TransportReceiver,
		Path:           "/dev/hidraw1",
	}

	frame := []byte{0x10, 0x20, 2, 0x00, 0x00}
	reading := decodeInterruptFrame(frame, candidate, p)
	if reading == nil {
		t.Fatal("expected reading")
	}

	if reading.Percentage != 50 {
		t.Fatalf("expected percentage 50, got %d", reading.Percentage)
	}
	if reading.Source != model.FrameSourceInterrupt {
		t.Fatalf("expected interrupt source, got %s", reading.Source)
	}

	badFrame := []byte{0x10, 0x20, 6, 0x00, 0x00}
	if decodeInterruptFrame(badFrame, candidate, p) != nil {
		t.Fatal("expected nil reading for bucket overflow")
	}
}
