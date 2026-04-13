//go:build linux

package profile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

func TestProfileSpecClassifyTransport(t *testing.T) {
	spec := ProfileSpec{
		TransportProductIDs: map[model.Transport]string{
			model.TransportReceiver:  "d030",
			model.TransportUSBDirect: "d044",
		},
	}

	transport := spec.ClassifyTransport("D030")
	if transport == nil {
		t.Fatalf("expected transport match")
	}

	if *transport != model.TransportReceiver {
		t.Fatalf("expected receiver transport, got %s", *transport)
	}
}

func TestEndpointSelectorMatches(t *testing.T) {
	if !(EndpointSelector{
		InterfaceNumbers:         []int{3},
		RequiredInputReportIDs:   []int{0x54},
		RequiredFeatureReportIDs: []int{0x51},
	}).Matches(model.HidCandidate{
		InterfaceNumber:  intPtr(3),
		InputReportIDs:   []int{0x54, 0x55},
		FeatureReportIDs: []int{0x51},
	}) {
		t.Fatal("expected selector to match candidate")
	}
}

func TestLoadEmbeddedProfilesLoadsKeychronM7(t *testing.T) {
	profiles, err := LoadEmbeddedProfiles()
	if err != nil {
		t.Fatalf("LoadEmbeddedProfiles() error = %v", err)
	}

	if len(profiles) == 0 {
		t.Fatal("expected embedded profiles to be loaded")
	}

	registry, err := BuildProfileRegistry(profiles)
	if err != nil {
		t.Fatalf("BuildProfileRegistry() error = %v", err)
	}

	m7, err := registry.GetProfile("keychron_m7")
	if err != nil {
		t.Fatalf("GetProfile(keychron_m7) error = %v", err)
	}

	if m7.SourcePath != "profiles/keychron_m7.yaml" {
		t.Fatalf("unexpected source path %q", m7.SourcePath)
	}
	if m7.Name != "Keychron M7" {
		t.Fatalf("unexpected name %q", m7.Name)
	}
	if m7.VendorID != "3434" {
		t.Fatalf("unexpected vendor id %q", m7.VendorID)
	}
	if got := m7.TransportProductIDs[model.TransportUSBDirect]; got != "d044" {
		t.Fatalf("unexpected usb_direct product id %q", got)
	}
	if m7.ProbePath != model.ProbePathFeatureOnly {
		t.Fatalf("unexpected probe path %q", m7.ProbePath)
	}
	if len(m7.WakeReport) != 33 {
		t.Fatalf("unexpected wake report length %d", len(m7.WakeReport))
	}
	if got := m7.DebugOutput().SourcePath; got != "profiles/keychron_m7.yaml" {
		t.Fatalf("unexpected debug source path %q", got)
	}
}

func TestLoadProfilesFSSortsFilesByName(t *testing.T) {
	profiles, err := loadProfilesFS(fstest.MapFS{
		"profiles/zulu.yaml":  {Data: []byte(validProfileYAML("zulu", "Zulu", "1000", "2000"))},
		"profiles/alpha.yaml": {Data: []byte(validProfileYAML("alpha", "Alpha", "1001", "2001"))},
	}, "profiles")
	if err != nil {
		t.Fatalf("loadProfilesFS() error = %v", err)
	}

	got := []string{profiles[0].ID, profiles[1].ID}
	want := []string{"alpha", "zulu"}
	if !slices.Equal(got, want) {
		t.Fatalf("profile load order = %v, want %v", got, want)
	}
}

func TestLoadProfilesFSRejectsOversizedFile(t *testing.T) {
	_, err := loadProfilesFS(fstest.MapFS{
		"profiles/too-big.yaml": {Data: make([]byte, maxCatalogProfileFileBytes+1)},
	}, "profiles")
	if err == nil {
		t.Fatal("expected oversized profile file error")
	}

	if !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestLoadProfileRegistryWithOverlayDirMissingAllowed(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "profiles")

	registry, exists, err := LoadProfileRegistryWithOverlayDir(overlayDir, true)
	if err != nil {
		t.Fatalf("LoadProfileRegistryWithOverlayDir() error = %v", err)
	}
	if exists {
		t.Fatal("expected missing overlay dir to report exists=false")
	}
	if _, err := registry.GetProfile("keychron_m7"); err != nil {
		t.Fatalf("expected embedded keychron_m7 profile, got %v", err)
	}
}

func TestLoadProfileRegistryWithOverlayDirRejectsMissingRequiredDir(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "profiles")

	_, _, err := LoadProfileRegistryWithOverlayDir(overlayDir, false)
	if err == nil {
		t.Fatal("expected missing explicit overlay dir error")
	}
	if !strings.Contains(err.Error(), "stat profile overlay directory") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestLoadProfileRegistryWithOverlayDirAllowsEmptyDirectory(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	registry, exists, err := LoadProfileRegistryWithOverlayDir(overlayDir, false)
	if err != nil {
		t.Fatalf("LoadProfileRegistryWithOverlayDir() error = %v", err)
	}
	if !exists {
		t.Fatal("expected existing overlay dir to report exists=true")
	}
	if _, err := registry.GetProfile("keychron_m7"); err != nil {
		t.Fatalf("expected embedded keychron_m7 profile, got %v", err)
	}
}

func TestLoadProfileRegistryWithOverlayDirOverlaysAndAddsProfiles(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	overridePath := filepath.Join(overlayDir, "keychron_m7.yaml")
	if err := os.WriteFile(overridePath, []byte(validProfileYAML("keychron_m7", "Local M7 Override", "d044", "d030")), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", overridePath, err)
	}

	additionPath := filepath.Join(overlayDir, "local-test.yaml")
	if err := os.WriteFile(additionPath, []byte(validProfileYAML("local-test", "Local Test Device", "1000", "2000")), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", additionPath, err)
	}

	registry, exists, err := LoadProfileRegistryWithOverlayDir(overlayDir, false)
	if err != nil {
		t.Fatalf("LoadProfileRegistryWithOverlayDir() error = %v", err)
	}
	if !exists {
		t.Fatal("expected overlay dir to report exists=true")
	}

	m7, err := registry.GetProfile("keychron_m7")
	if err != nil {
		t.Fatalf("GetProfile(keychron_m7) error = %v", err)
	}
	if m7.Name != "Local M7 Override" {
		t.Fatalf("expected local keychron_m7 override, got %q", m7.Name)
	}
	if m7.SourcePath != overridePath {
		t.Fatalf("unexpected keychron_m7 source path %q", m7.SourcePath)
	}

	localProfile, err := registry.GetProfile("local-test")
	if err != nil {
		t.Fatalf("GetProfile(local-test) error = %v", err)
	}
	if localProfile.SourcePath != additionPath {
		t.Fatalf("unexpected local-test source path %q", localProfile.SourcePath)
	}

	allProfiles, err := registry.SelectProfiles(nil)
	if err != nil {
		t.Fatalf("SelectProfiles(nil) error = %v", err)
	}
	if allProfiles[len(allProfiles)-1].ID != "local-test" {
		t.Fatalf("expected local-test to append after embedded profiles, got last profile %q", allProfiles[len(allProfiles)-1].ID)
	}
}

func TestLoadProfileRegistryWithOverlayDirRejectsDuplicateLocalIDs(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	firstPath := filepath.Join(overlayDir, "alpha.yaml")
	if err := os.WriteFile(firstPath, []byte(validProfileYAML("local-dup", "Local One", "1000", "2000")), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", firstPath, err)
	}

	secondPath := filepath.Join(overlayDir, "beta.yaml")
	if err := os.WriteFile(secondPath, []byte(validProfileYAML("LOCAL-DUP", "Local Two", "1001", "2001")), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", secondPath, err)
	}

	_, _, err := LoadProfileRegistryWithOverlayDir(overlayDir, false)
	if err == nil {
		t.Fatal("expected duplicate local profile id error")
	}
	if !strings.Contains(err.Error(), firstPath) || !strings.Contains(err.Error(), secondPath) {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestDecodeProfileYAMLRejectsUnknownField(t *testing.T) {
	_, err := decodeProfileYAML("profiles/test.yaml", []byte(validProfileYAML("test", "Test", "1234", "5678")+"\nunknown_field: true\n"))
	if err == nil {
		t.Fatal("expected unknown field error")
	}

	if !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestParseTransportProductIDsRejectsDuplicateIDs(t *testing.T) {
	raw := map[string]string{
		string(model.TransportUSBDirect): "0x1234",
		string(model.TransportReceiver):  "1234",
	}

	_, err := parseTransportProductIDs(raw)
	if err == nil {
		t.Fatalf("expected duplicate product ID error")
	}

	if !strings.Contains(err.Error(), "transport_product_ids") {
		t.Fatalf("expected error to reference transport_product_ids, got %q", err.Error())
	}
}

func TestParseTransportProductIDsDistinctIDs(t *testing.T) {
	raw := map[string]string{
		string(model.TransportUSBDirect): "0xABCD",
		string(model.TransportReceiver):  "1234",
	}

	parsed, err := parseTransportProductIDs(raw)
	if err != nil {
		t.Fatalf("parseTransportProductIDs() error = %v", err)
	}

	if got := parsed[model.TransportUSBDirect]; got != "abcd" {
		t.Fatalf("expected usb_direct product ID to normalize to abcd, got %q", got)
	}
	if got := parsed[model.TransportReceiver]; got != "1234" {
		t.Fatalf("expected receiver product ID to be 1234, got %q", got)
	}
}

func TestParseTransportProductIDsInvalidHex(t *testing.T) {
	raw := map[string]string{
		string(model.TransportUSBDirect): "0xZZZZ",
	}

	_, err := parseTransportProductIDs(raw)
	if err == nil {
		t.Fatalf("expected invalid hex error")
	}

	if !strings.Contains(err.Error(), "transport_product_ids.usb_direct") {
		t.Fatalf("expected error to reference transport_product_ids.usb_direct, got %q", err.Error())
	}
}

func TestBuildProfileRegistryRejectsDuplicateIDs(t *testing.T) {
	_, err := BuildProfileRegistry([]ProfileSpec{
		{ID: "m7", SourcePath: "profiles/first.yaml"},
		{ID: "M7", SourcePath: "profiles/second.yaml"},
	})
	if err == nil {
		t.Fatal("expected duplicate profile id error")
	}

	if !strings.Contains(err.Error(), "duplicate profile id") ||
		!strings.Contains(err.Error(), "profiles/first.yaml") ||
		!strings.Contains(err.Error(), "profiles/second.yaml") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestSelectProfilesHandlesNilAndUnknown(t *testing.T) {
	registry, err := BuildProfileRegistry([]ProfileSpec{
		{ID: "alpha", SourcePath: "profiles/alpha.yaml"},
		{ID: "beta", SourcePath: "profiles/beta.yaml"},
	})
	if err != nil {
		t.Fatalf("BuildProfileRegistry() error = %v", err)
	}

	all, err := registry.SelectProfiles(nil)
	if err != nil {
		t.Fatalf("SelectProfiles(nil) error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected all profiles, got %d", len(all))
	}

	selectedID := "beta"
	selected, err := registry.SelectProfiles(&selectedID)
	if err != nil {
		t.Fatalf("SelectProfiles(beta) error = %v", err)
	}
	if len(selected) != 1 || selected[0].ID != "beta" {
		t.Fatalf("unexpected selected profiles %#v", selected)
	}

	unknownID := "missing"
	_, err = registry.SelectProfiles(&unknownID)
	if err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestConvertProfileRejectsInvalidProbeShape(t *testing.T) {
	_, err := decodeProfileYAML("profiles/bad.yaml", []byte(validProfileYAML("bad", "Bad", "1234", "5678")+"\nquery_length: 1\n"))
	if err == nil {
		t.Fatal("expected invalid query length error")
	}

	if !strings.Contains(err.Error(), "query_length") {
		t.Fatalf("unexpected error %q", err)
	}
}

func validProfileYAML(id, name, usbProductID, receiverProductID string) string {
	return "" +
		"id: " + id + "\n" +
		"name: " + name + "\n" +
		"vendor_id: 3434\n" +
		"transport_product_ids:\n" +
		"  usb_direct: " + usbProductID + "\n" +
		"  receiver: " + receiverProductID + "\n" +
		"device_type: mouse\n" +
		"icon_name: input-mouse\n" +
		"probe_path: feature_or_interrupt\n" +
		"wake_report_hex: 00b200000000000000000000000000000000000000000000000000000000000000\n" +
		"query_report_id: 81\n" +
		"prime_query_cmd: 7\n" +
		"battery_query_cmd: 6\n" +
		"query_length: 21\n" +
		"expected_signature_hex: 343444d0\n" +
		"battery_offset: 11\n" +
		"status_offset: 12\n" +
		"fallback_input_report_id: 84\n" +
		"fallback_input_cmd: 228\n" +
		"fallback_input_length: 21\n" +
		"fallback_battery_offset: 2\n" +
		"fallback_battery_bucket_max: 4\n" +
		"charging_status_bytes: [1]\n" +
		"query_endpoint:\n" +
		"  required_feature_report_ids: [81]\n" +
		"wake_endpoint:\n" +
		"  interface_numbers: [3]\n"
}

func intPtr(v int) *int {
	return &v
}
