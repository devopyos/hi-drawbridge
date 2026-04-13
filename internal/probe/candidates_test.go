//go:build linux

package probe

import (
	"reflect"
	"testing"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func TestBuildProbeTargetsSelectsWakeCandidate(t *testing.T) {
	interfaceOne := 1
	interfaceThree := 3

	candidates := []model.HidCandidate{
		{
			StableDeviceID:   "dev1",
			Transport:        model.TransportUSBDirect,
			InterfaceNumber:  &interfaceOne,
			Path:             "/dev/hidraw0",
			FeatureReportIDs: []int{0x01},
		},
		{
			StableDeviceID:   "dev1",
			Transport:        model.TransportUSBDirect,
			InterfaceNumber:  &interfaceThree,
			Path:             "/dev/hidraw1",
			FeatureReportIDs: []int{0x01},
		},
	}

	p := profile.ProfileSpec{
		QueryEndpoint: profile.EndpointSelector{
			InterfaceNumbers: []int{1},
		},
		WakeEndpoint: profile.EndpointSelector{
			InterfaceNumbers: []int{3},
		},
	}

	targets := BuildProbeTargets(candidates, p)
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	target := targets[0]
	if target.QueryCandidate.Path != "/dev/hidraw0" {
		t.Fatalf("expected query candidate /dev/hidraw0, got %s", target.QueryCandidate.Path)
	}
	if target.WakeCandidate.Path != "/dev/hidraw1" {
		t.Fatalf("expected wake candidate /dev/hidraw1, got %s", target.WakeCandidate.Path)
	}
}

func TestSortCandidatesByTransportRank(t *testing.T) {
	interfaceTwo := 2
	interfaceOne := 1

	candidates := []model.HidCandidate{
		{
			StableDeviceID:  "dev1",
			Transport:       model.TransportUSBDirect,
			InterfaceNumber: &interfaceTwo,
			Path:            "/dev/hidraw0",
		},
		{
			StableDeviceID:  "dev2",
			Transport:       model.TransportReceiver,
			InterfaceNumber: &interfaceOne,
			Path:            "/dev/hidraw1",
		},
	}

	settings := config.Settings{
		PreferredTransports: []model.Transport{model.TransportReceiver, model.TransportUSBDirect},
	}

	sorted := SortCandidates(candidates, settings)
	if len(sorted) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(sorted))
	}

	if sorted[0].Transport != model.TransportReceiver {
		t.Fatalf("expected receiver first, got %s", sorted[0].Transport)
	}
}

func TestBuildProbeTargetsDeterministicOrder(t *testing.T) {
	candidates := []model.HidCandidate{
		{
			StableDeviceID: "b-device",
			Transport:      model.TransportUSBDirect,
			Path:           "/dev/hidraw2",
		},
		{
			StableDeviceID: "a-device",
			Transport:      model.TransportUSBDirect,
			Path:           "/dev/hidraw1",
		},
	}

	p := profile.ProfileSpec{}

	targets := BuildProbeTargets(candidates, p)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}

	got := []string{targets[0].DeviceID, targets[1].DeviceID}
	want := []string{"a-device", "b-device"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected deterministic order %v, got %v", want, got)
	}
}

func TestSortTargetsBy(t *testing.T) {
	iface3 := 3
	iface1 := 1
	targets := []model.ProbeTarget{
		{
			QueryCandidate: model.HidCandidate{
				Transport:       model.TransportUSBDirect,
				InterfaceNumber: &iface3,
				Path:            "/dev/hidraw9",
			},
		},
		{
			QueryCandidate: model.HidCandidate{
				Transport:       model.TransportReceiver,
				InterfaceNumber: &iface1,
				Path:            "/dev/hidraw1",
			},
		},
	}

	ranking := map[model.Transport]int{
		model.TransportReceiver:  0,
		model.TransportUSBDirect: 1,
	}

	sortTargetsBy(targets, ranking)
	if targets[0].QueryCandidate.Transport != model.TransportReceiver {
		t.Fatalf("expected receiver target first, got %s", targets[0].QueryCandidate.Transport)
	}
}
