//go:build linux

package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func testProfileSpec() profile.ProfileSpec {
	return profile.ProfileSpec{
		VendorID: "3434",
		TransportProductIDs: map[model.Transport]string{
			model.TransportUSBDirect: "d044",
			model.TransportReceiver:  "d030",
		},
		QueryEndpoint: profile.EndpointSelector{
			RequiredFeatureReportIDs: []int{0x51},
		},
		WakeEndpoint: profile.EndpointSelector{
			InterfaceNumbers: []int{3},
		},
	}
}

func withDiscoveryTestEnvironment(t *testing.T, sysfsRoot, devRoot string) {
	t.Helper()

	prevReadFile := discoveryReadFile
	prevStat := discoveryStat
	prevEvalLinks := discoveryEvalLinks
	prevGlob := discoveryGlob
	prevSysfsRoot := discoverySysfsRoot
	prevDevRoot := discoveryDevRoot

	discoveryReadFile = os.ReadFile
	discoveryStat = os.Stat
	discoveryEvalLinks = filepath.EvalSymlinks
	discoveryGlob = filepath.Glob
	discoverySysfsRoot = sysfsRoot
	discoveryDevRoot = devRoot

	t.Cleanup(func() {
		discoveryReadFile = prevReadFile
		discoveryStat = prevStat
		discoveryEvalLinks = prevEvalLinks
		discoveryGlob = prevGlob
		discoverySysfsRoot = prevSysfsRoot
		discoveryDevRoot = prevDevRoot
	})
}

type stubFileInfo struct {
	mode os.FileMode
}

func (s stubFileInfo) Name() string       { return "stub" }
func (s stubFileInfo) Size() int64        { return 0 }
func (s stubFileInfo) Mode() os.FileMode  { return s.mode }
func (s stubFileInfo) ModTime() time.Time { return time.Time{} }
func (s stubFileInfo) IsDir() bool        { return s.mode.IsDir() }
func (s stubFileInfo) Sys() any           { return nil }

type testDiscoveryEntry struct {
	hidrawName      string
	hidID           string
	hidName         string
	hidPhys         *string
	descriptor      []byte
	interfaceValue  *string
	resolvedDevPath string
}

func writeDiscoveryEntry(t *testing.T, sysfsRoot string, entry testDiscoveryEntry) string {
	t.Helper()

	entryPath := filepath.Join(sysfsRoot, entry.hidrawName)
	if err := os.MkdirAll(entryPath, 0o755); err != nil {
		t.Fatalf("mkdir entry path: %v", err)
	}

	deviceTarget := entry.resolvedDevPath
	if deviceTarget == "" {
		deviceTarget = filepath.Join(
			sysfsRoot,
			"devices",
			entry.hidrawName,
			"input",
			"input3",
			"hidraw",
			entry.hidrawName,
		)
	}
	if err := os.MkdirAll(deviceTarget, 0o755); err != nil {
		t.Fatalf("mkdir device target: %v", err)
	}

	deviceLink := filepath.Join(entryPath, "device")
	if err := os.Symlink(deviceTarget, deviceLink); err != nil {
		t.Fatalf("symlink device target: %v", err)
	}

	ueventLines := []string{}
	if entry.hidID != "" {
		ueventLines = append(ueventLines, "HID_ID="+entry.hidID)
	}
	if entry.hidName != "" {
		ueventLines = append(ueventLines, "HID_NAME="+entry.hidName)
	}
	if entry.hidPhys != nil {
		ueventLines = append(ueventLines, "HID_PHYS="+*entry.hidPhys)
	}
	ueventData := strings.Join(ueventLines, "\n")
	if ueventData != "" {
		ueventData += "\n"
	}

	if err := os.WriteFile(filepath.Join(deviceTarget, "uevent"), []byte(ueventData), 0o644); err != nil {
		t.Fatalf("write uevent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deviceTarget, "report_descriptor"), entry.descriptor, 0o644); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	if entry.interfaceValue != nil {
		if err := os.WriteFile(filepath.Join(deviceTarget, "bInterfaceNumber"), []byte(*entry.interfaceValue), 0o644); err != nil {
			t.Fatalf("write interface number: %v", err)
		}
	}

	return entryPath
}

func writeDevNode(t *testing.T, devRoot, hidrawName string) string {
	t.Helper()

	if err := os.MkdirAll(devRoot, 0o755); err != nil {
		t.Fatalf("mkdir dev root: %v", err)
	}

	hidrawPath := filepath.Join(devRoot, hidrawName)
	if err := os.WriteFile(hidrawPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write hidraw node: %v", err)
	}

	return hidrawPath
}

func TestParseHidID(t *testing.T) {
	tests := []struct {
		name        string
		hidID       string
		wantVendor  string
		wantProduct string
		wantErr     string
	}{
		{
			name:        "valid normalizes last four digits",
			hidID:       "0003:00003434:0000D044",
			wantVendor:  "3434",
			wantProduct: "d044",
		},
		{
			name:    "invalid part count",
			hidID:   "0003:3434",
			wantErr: `invalid HID_ID value: "0003:3434"`,
		},
		{
			name:    "vendor too short",
			hidID:   "0003:343:D044",
			wantErr: `invalid HID_ID vendor component "343"`,
		},
		{
			name:    "product not hex",
			hidID:   "0003:3434:ZZ44",
			wantErr: `invalid HID_ID product component "ZZ44"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vendorID, productID, err := parseHidID(tt.hidID)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if vendorID != tt.wantVendor || productID != tt.wantProduct {
				t.Fatalf("parseHidID(%q) = (%q, %q), want (%q, %q)", tt.hidID, vendorID, productID, tt.wantVendor, tt.wantProduct)
			}
		})
	}
}

func TestParseReportDescriptor(t *testing.T) {
	tests := []struct {
		name           string
		descriptor     []byte
		wantInputIDs   []int
		wantFeatureIDs []int
	}{
		{
			name:           "feature and input use report id",
			descriptor:     []byte{0x85, 0x51, 0x81, 0x02, 0xB1, 0x02},
			wantInputIDs:   []int{0x51},
			wantFeatureIDs: []int{0x51},
		},
		{
			name:           "default report id is zero",
			descriptor:     []byte{0x81, 0x02, 0xB1, 0x02},
			wantInputIDs:   []int{0x00},
			wantFeatureIDs: []int{0x00},
		},
		{
			name:           "long item is skipped",
			descriptor:     []byte{0xFE, 0x02, 0xAA, 0x01, 0x02, 0x85, 0x54, 0x81, 0x02},
			wantInputIDs:   []int{0x54},
			wantFeatureIDs: nil,
		},
		{
			name:           "truncated long item stops parsing safely",
			descriptor:     []byte{0x85, 0x51, 0xFE, 0x02},
			wantInputIDs:   nil,
			wantFeatureIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputIDs, featureIDs := ParseReportDescriptor(tt.descriptor)
			if !slicesEqual(inputIDs, tt.wantInputIDs) {
				t.Fatalf("input IDs = %v, want %v", inputIDs, tt.wantInputIDs)
			}
			if !slicesEqual(featureIDs, tt.wantFeatureIDs) {
				t.Fatalf("feature IDs = %v, want %v", featureIDs, tt.wantFeatureIDs)
			}
		})
	}
}

func TestReadInterfaceNumber(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		base := t.TempDir()
		deviceLink := filepath.Join(base, "device")
		if err := os.MkdirAll(deviceLink, 0o755); err != nil {
			t.Fatalf("mkdir device link: %v", err)
		}

		devicePath := filepath.Join(deviceLink, "bInterfaceNumber")
		if err := os.WriteFile(devicePath, []byte("0a\n"), 0o644); err != nil {
			t.Fatalf("write device bInterfaceNumber: %v", err)
		}

		val := readInterfaceNumber(deviceLink)
		if val == nil {
			t.Fatalf("expected interface number")
		}
		if *val != 10 {
			t.Fatalf("expected 10, got %d", *val)
		}
	})

	t.Run("missing", func(t *testing.T) {
		base := t.TempDir()
		deviceLink := filepath.Join(base, "device")
		if err := os.MkdirAll(deviceLink, 0o755); err != nil {
			t.Fatalf("mkdir device link: %v", err)
		}

		val := readInterfaceNumber(deviceLink)
		if val != nil {
			t.Fatalf("expected nil, got %d", *val)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		base := t.TempDir()
		deviceLink := filepath.Join(base, "device")
		if err := os.MkdirAll(deviceLink, 0o755); err != nil {
			t.Fatalf("mkdir device link: %v", err)
		}

		devicePath := filepath.Join(deviceLink, "bInterfaceNumber")
		if err := os.WriteFile(devicePath, []byte("zz"), 0o644); err != nil {
			t.Fatalf("write device bInterfaceNumber: %v", err)
		}

		val := readInterfaceNumber(deviceLink)
		if val != nil {
			t.Fatalf("expected nil, got %d", *val)
		}
	})
}

func TestEndpointMatchesBestEffort(t *testing.T) {
	t.Run("interface present mismatch rejects", func(t *testing.T) {
		iface := 2
		selector := profile.EndpointSelector{
			InterfaceNumbers:         []int{3},
			RequiredFeatureReportIDs: []int{0x51},
		}

		match := endpointMatchesBestEffort(selector, &iface, nil, []int{0x51})
		if match {
			t.Fatalf("expected mismatch to reject candidate")
		}
	})

	t.Run("interface missing allows report match", func(t *testing.T) {
		selector := profile.EndpointSelector{
			InterfaceNumbers:         []int{3},
			RequiredFeatureReportIDs: []int{0x51},
		}

		match := endpointMatchesBestEffort(selector, nil, nil, []int{0x51})
		if !match {
			t.Fatalf("expected match when interface is missing")
		}
	})
}

func TestNormalizedDeviceSourceBlankPhysFallsBackToResolvedPath(t *testing.T) {
	tempDir := t.TempDir()
	resolved := filepath.Join(tempDir, "devices", "usb0", "input", "input3", "hidraw", "hidraw0")

	prevEvalLinks := discoveryEvalLinks
	discoveryEvalLinks = func(string) (string, error) {
		return resolved, nil
	}
	t.Cleanup(func() {
		discoveryEvalLinks = prevEvalLinks
	})

	blank := " \n "
	gotBlank := normalizedDeviceSource(&blank, "/ignored")
	gotNil := normalizedDeviceSource(nil, "/ignored")
	if gotBlank != gotNil {
		t.Fatalf("expected blank HID_PHYS to behave like nil; got %q vs %q", gotBlank, gotNil)
	}

	phys := "usb-0000:00:14.0-2/input3\n"
	gotPhys := normalizedDeviceSource(&phys, "/ignored")
	if gotPhys != "usb-0000:00:14.0-2" {
		t.Fatalf("expected trimmed HID_PHYS source, got %q", gotPhys)
	}
}

func TestCandidateFromEntrySuccessUsesResolvedFallbackAndConfiguredDevRoot(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	ifaceValue := "03\n"
	blankPhys := "   "
	entryPath := writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName:     "hidraw0",
		hidID:          "0003:3434:D044",
		hidName:        "Keychron M7",
		hidPhys:        &blankPhys,
		descriptor:     []byte{0x85, 0x51, 0xB1, 0x02},
		interfaceValue: &ifaceValue,
	})
	expectedPath := writeDevNode(t, devRoot, "hidraw0")

	candidate, matched, err := candidateFromEntry(entryPath, "", testProfileSpec())
	if err != nil {
		t.Fatalf("candidateFromEntry() error = %v", err)
	}
	if !matched || candidate == nil {
		t.Fatalf("expected candidate")
	}
	if candidate.Path != expectedPath {
		t.Fatalf("expected candidate path %q, got %q", expectedPath, candidate.Path)
	}
	if candidate.HidrawName != "hidraw0" {
		t.Fatalf("expected hidraw0, got %q", candidate.HidrawName)
	}
	if candidate.StableDeviceID != stableDeviceID(nil, filepath.Join(entryPath, "device")) {
		t.Fatalf("expected blank HID_PHYS to fall back to resolved device path")
	}
	if candidate.InterfaceNumber == nil || *candidate.InterfaceNumber != 3 {
		t.Fatalf("expected interface number 3, got %v", candidate.InterfaceNumber)
	}
}

func TestCandidateFromEntryFiltersNonMatchingCandidate(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	ifaceValue := "02\n"
	entryPath := writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName:     "hidraw1",
		hidID:          "0003:3434:D044",
		hidName:        "Keychron M7",
		descriptor:     []byte{0x81, 0x02},
		interfaceValue: &ifaceValue,
	})
	writeDevNode(t, devRoot, "hidraw1")

	candidate, matched, err := candidateFromEntry(entryPath, "", testProfileSpec())
	if err != nil {
		t.Fatalf("candidateFromEntry() error = %v", err)
	}
	if matched || candidate != nil {
		t.Fatalf("expected non-matching candidate to be skipped, got matched=%v candidate=%#v", matched, candidate)
	}
}

func TestCandidateFromEntryReportsMalformedHIDID(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	entryPath := writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName: "hidraw2",
		hidID:      "0003:34ZZ:D044",
		descriptor: []byte{0x85, 0x51, 0xB1, 0x02},
	})
	writeDevNode(t, devRoot, "hidraw2")

	candidate, matched, err := candidateFromEntry(entryPath, "", testProfileSpec())
	if err == nil {
		t.Fatalf("expected error")
	}
	if matched {
		t.Fatal("expected malformed HID_ID candidate not to match")
	}
	if candidate != nil {
		t.Fatalf("expected nil candidate on malformed HID_ID")
	}
	if !strings.Contains(err.Error(), `parse HID_ID "0003:34ZZ:D044"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiscoverHidrawCandidatesDefaultScanIncludesDiagnostics(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	ifaceValue := "03\n"
	writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName:     "hidraw9",
		hidID:          "0003:3434:D044",
		hidName:        "Keychron M7",
		descriptor:     []byte{0x85, 0x51, 0xB1, 0x02},
		interfaceValue: &ifaceValue,
	})
	writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName: "hidraw2",
		hidID:      "0003:34ZZ:D044",
		descriptor: []byte{0x85, 0x51, 0xB1, 0x02},
	})
	writeDevNode(t, devRoot, "hidraw9")
	writeDevNode(t, devRoot, "hidraw2")

	candidates, diagnostics := DiscoverHidrawCandidates(context.Background(), config.Settings{}, testProfileSpec())
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].HidrawName != "hidraw9" {
		t.Fatalf("expected valid candidate hidraw9, got %q", candidates[0].HidrawName)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}
	if diagnostics[0].EntryPath != filepath.Join(sysfsRoot, "hidraw2") {
		t.Fatalf("unexpected diagnostic entry path: %q", diagnostics[0].EntryPath)
	}
	if diagnostics[0].Error == nil || !strings.Contains(diagnostics[0].Error.Error(), "parse HID_ID") {
		t.Fatalf("unexpected diagnostic error: %v", diagnostics[0].Error)
	}
}

func TestDiscoverHidrawCandidatesCancellationUsesRootScopedDiagnostic(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName: "hidraw0",
		hidID:      "0003:3434:D044",
		descriptor: []byte{0x85, 0x51, 0xB1, 0x02},
	})
	writeDevNode(t, devRoot, "hidraw0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	candidates, diagnostics := DiscoverHidrawCandidates(ctx, config.Settings{}, testProfileSpec())
	if len(candidates) != 0 {
		t.Fatalf("expected no candidates, got %d", len(candidates))
	}
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}
	if diagnostics[0].EntryPath != sysfsRoot {
		t.Fatalf("expected root-scoped diagnostic entry %q, got %q", sysfsRoot, diagnostics[0].EntryPath)
	}
	if diagnostics[0].Error == nil || !strings.Contains(diagnostics[0].Error.Error(), "before processing remaining entries") {
		t.Fatalf("unexpected diagnostic error: %v", diagnostics[0].Error)
	}
}

func TestDiscoverHidrawCandidatesForceHidrawPreservesForcedPath(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	ifaceValue := "03\n"
	writeDiscoveryEntry(t, sysfsRoot, testDiscoveryEntry{
		hidrawName:     "hidraw7",
		hidID:          "0003:3434:D044",
		hidName:        "Keychron M7",
		descriptor:     []byte{0x85, 0x51, 0xB1, 0x02},
		interfaceValue: &ifaceValue,
	})

	forcedDir := filepath.Join(base, "custom-dev")
	if err := os.MkdirAll(forcedDir, 0o755); err != nil {
		t.Fatalf("mkdir forced dir: %v", err)
	}
	forcedPath := filepath.Join(forcedDir, "hidraw7")
	if err := os.WriteFile(forcedPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write forced hidraw: %v", err)
	}

	prevStat := discoveryStat
	discoveryStat = func(path string) (os.FileInfo, error) {
		if path == forcedPath {
			return stubFileInfo{mode: os.ModeCharDevice}, nil
		}
		return prevStat(path)
	}
	t.Cleanup(func() {
		discoveryStat = prevStat
	})

	settings := config.Settings{ForceHidraw: &forcedPath}
	candidates, diagnostics := DiscoverHidrawCandidates(context.Background(), settings, testProfileSpec())
	if len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostics, got %v", diagnostics)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Path != forcedPath {
		t.Fatalf("expected forced hidraw path %q, got %q", forcedPath, candidates[0].Path)
	}
}

func TestDiscoverHidrawCandidatesReportsGlobFailure(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	prevGlob := discoveryGlob
	discoveryGlob = func(string) ([]string, error) {
		return nil, errors.New("glob failed")
	}
	t.Cleanup(func() {
		discoveryGlob = prevGlob
	})

	candidates, diagnostics := DiscoverHidrawCandidates(context.Background(), config.Settings{}, testProfileSpec())
	if len(candidates) != 0 {
		t.Fatalf("expected no candidates, got %d", len(candidates))
	}
	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}
	if diagnostics[0].EntryPath != filepath.Join(sysfsRoot, "hidraw*") {
		t.Fatalf("unexpected diagnostic entry path: %q", diagnostics[0].EntryPath)
	}
	if diagnostics[0].Error == nil || !strings.Contains(diagnostics[0].Error.Error(), "glob failed") {
		t.Fatalf("unexpected diagnostic error: %v", diagnostics[0].Error)
	}
}

func TestForcedHidrawSysfsEntryValidation(t *testing.T) {
	base := t.TempDir()
	sysfsRoot := filepath.Join(base, "sys", "class", "hidraw")
	devRoot := filepath.Join(base, "dev")
	withDiscoveryTestEnvironment(t, sysfsRoot, devRoot)

	if err := os.MkdirAll(filepath.Join(sysfsRoot, "hidraw3"), 0o755); err != nil {
		t.Fatalf("mkdir sysfs entry: %v", err)
	}

	forcedPath := filepath.Join(base, "custom", "hidraw3")
	prevStat := discoveryStat
	discoveryStat = func(path string) (os.FileInfo, error) {
		if path == forcedPath {
			return stubFileInfo{mode: os.ModeCharDevice}, nil
		}
		return prevStat(path)
	}
	t.Cleanup(func() {
		discoveryStat = prevStat
	})

	sysfsEntry, hidrawPath, ok := forcedHidrawSysfsEntry(forcedPath)
	if !ok {
		t.Fatalf("expected forced hidraw path to resolve")
	}
	if sysfsEntry != filepath.Join(sysfsRoot, "hidraw3") {
		t.Fatalf("unexpected sysfs entry: %q", sysfsEntry)
	}
	if hidrawPath != forcedPath {
		t.Fatalf("unexpected hidraw path: %q", hidrawPath)
	}
}

func slicesEqual(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}
