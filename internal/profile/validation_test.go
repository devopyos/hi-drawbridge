//go:build linux

package profile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

func TestLoadProfileRegistry(t *testing.T) {
	registry, err := LoadProfileRegistry()
	if err != nil {
		t.Fatalf("LoadProfileRegistry() error = %v", err)
	}

	if len(registry.Profiles) == 0 {
		t.Fatal("expected embedded profile registry to be non-empty")
	}
	if _, err := registry.GetProfile("keychron_m7"); err != nil {
		t.Fatalf("expected embedded keychron_m7 profile, got %v", err)
	}
	if len(registry.DebugOutput()) != len(registry.Profiles) {
		t.Fatalf("unexpected DebugOutput size %d", len(registry.DebugOutput()))
	}
}

func TestLoadProfilesFSEmptyAndNoYAML(t *testing.T) {
	if _, err := loadProfilesFS(fstest.MapFS{}, "profiles"); err == nil {
		t.Fatal("expected empty catalog error")
	}

	if _, err := loadProfilesFS(fstest.MapFS{
		"profiles/readme.txt": {Data: []byte("ignore me")},
		"profiles/subdir/file.yaml": {
			Data: []byte(validProfileYAML("nested", "Nested", "1234", "5678")),
		},
	}, "profiles"); err == nil || !strings.Contains(err.Error(), "contains no .yaml files") {
		t.Fatalf("expected no-yaml-files error, got %v", err)
	}
}

func TestReadProfileFileMissing(t *testing.T) {
	if _, err := readProfileFile(fstest.MapFS{}, "profiles/missing.yaml"); err == nil {
		t.Fatal("expected missing profile file error")
	}
}

func TestDecodeProfileYAMLRejectsMultipleDocuments(t *testing.T) {
	_, err := decodeProfileYAML("profiles/test.yaml", []byte(validProfileYAML("test", "Test", "1234", "5678")+"---\n"+validProfileYAML("other", "Other", "1235", "5679")))
	if err == nil {
		t.Fatal("expected multiple documents error")
	}
}

func TestConvertProfileRejectsInvalidProbePath(t *testing.T) {
	_, err := decodeProfileYAML("profiles/bad.yaml", []byte(strings.Replace(validProfileYAML("bad", "Bad", "1234", "5678"), "feature_or_interrupt", "bogus_path", 1)))
	if err == nil {
		t.Fatal("expected invalid probe path error")
	}
}

func TestRequireHelpersAndDecoders(t *testing.T) {
	if _, err := requireString("id", nil); err == nil {
		t.Fatal("expected missing string error")
	}
	if _, err := requireString("id", strPtr("   ")); err == nil {
		t.Fatal("expected empty string error")
	}
	if _, err := requireInt("value", nil); err == nil {
		t.Fatal("expected missing int error")
	}
	if _, err := requireTransportProductIDs("transport_product_ids", nil); err == nil {
		t.Fatal("expected missing transport ids error")
	}
	if _, err := requireEndpointSelector("query_endpoint", nil); err == nil {
		t.Fatal("expected missing endpoint selector error")
	}
	if _, err := decodeRequiredHexBytes("wake_report_hex", strPtr("zz"), maxWakeReportBytes); err == nil {
		t.Fatal("expected invalid hex bytes error")
	}
	if _, err := decodeRequiredHexBytes("wake_report_hex", strPtr(""), maxWakeReportBytes); err == nil {
		t.Fatal("expected empty hex bytes error")
	}
	if _, err := parseHexID("vendor_id", "xyz"); err == nil {
		t.Fatal("expected invalid hex id error")
	}

	extractor := &fieldExtractor{err: errors.New("stop")}
	if got := extractor.String("id", strPtr("value")); got != "" {
		t.Fatalf("expected short-circuited string extractor, got %q", got)
	}
	if got := extractor.Int("query_report_id", intPtr(1)); got != 0 {
		t.Fatalf("expected short-circuited int extractor, got %d", got)
	}
	if got := extractor.HexID("vendor_id", strPtr("3434")); got != "" {
		t.Fatalf("expected short-circuited hex id extractor, got %q", got)
	}
	if got := extractor.TransportProductIDs("transport_product_ids", &map[string]string{"usb_direct": "1234"}); got != nil {
		t.Fatalf("expected short-circuited transport extractor, got %#v", got)
	}
	if got := extractor.HexBytes("wake_report_hex", strPtr("00"), maxWakeReportBytes); got != nil {
		t.Fatalf("expected short-circuited hex bytes extractor, got %#v", got)
	}
	if got := extractor.Endpoint("query_endpoint", &yamlEndpointSelector{}); !reflect.DeepEqual(got, yamlEndpointSelector{}) {
		t.Fatalf("expected short-circuited endpoint extractor, got %#v", got)
	}
}

func TestValidateProfileSpecFailures(t *testing.T) {
	valid := validSpec()
	if err := validateProfileSpec(valid); err != nil {
		t.Fatalf("validateProfileSpec(valid) error = %v", err)
	}

	cases := []struct {
		name string
		mut  func(ProfileSpec) ProfileSpec
	}{
		{
			name: "missing name",
			mut: func(p ProfileSpec) ProfileSpec {
				p.Name = ""
				return p
			},
		},
		{
			name: "negative battery offset",
			mut: func(p ProfileSpec) ProfileSpec {
				p.BatteryOffset = -1
				return p
			},
		},
		{
			name: "invalid query report id",
			mut: func(p ProfileSpec) ProfileSpec {
				p.QueryReportID = 256
				return p
			},
		},
		{
			name: "invalid query endpoint selector",
			mut: func(p ProfileSpec) ProfileSpec {
				p.QueryEndpoint.RequiredFeatureReportIDs = []int{256}
				return p
			},
		},
		{
			name: "invalid query length",
			mut: func(p ProfileSpec) ProfileSpec {
				p.QueryLength = 1
				return p
			},
		},
		{
			name: "invalid fallback length for interrupt path",
			mut: func(p ProfileSpec) ProfileSpec {
				p.ProbePath = model.ProbePathInterruptOnly
				p.FallbackInputLength = 1
				return p
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateProfileSpec(tc.mut(valid)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	requiredInvalid := valid
	requiredInvalid.IconName = ""
	if err := validateProfileRequiredFields(requiredInvalid); err == nil {
		t.Fatal("expected validateProfileRequiredFields to reject empty icon")
	}

	requiredInvalid = valid
	requiredInvalid.WakeReport = make([]byte, maxWakeReportBytes+1)
	if err := validateProfileRequiredFields(requiredInvalid); err == nil {
		t.Fatal("expected validateProfileRequiredFields to reject oversized wake report")
	}

	offsetInvalid := valid
	offsetInvalid.FallbackBatteryBucketMax = -1
	if err := validateProfileOffsetFields(offsetInvalid); err == nil {
		t.Fatal("expected validateProfileOffsetFields to reject negative bucket max")
	}

	byteInvalid := valid
	byteInvalid.ChargingStatusBytes = []int{256}
	if err := validateProfileByteFields(byteInvalid); err == nil {
		t.Fatal("expected validateProfileByteFields to reject invalid charging byte")
	}

	selectorInvalid := valid
	selectorInvalid.WakeEndpoint.InterfaceNumbers = []int{-1}
	if err := validateProfileSelectorFields(selectorInvalid); err == nil {
		t.Fatal("expected validateProfileSelectorFields to reject negative interface number")
	}
}

func TestMinQueryLengthForProbePathAndByteValidation(t *testing.T) {
	if got, err := minQueryLengthForProbePath(model.ProbePathFeatureOnly); err != nil || got != 2 {
		t.Fatalf("unexpected feature_only min query length: %d, %v", got, err)
	}
	if got, err := minQueryLengthForProbePath(model.ProbePathInterruptOnly); err != nil || got != 3 {
		t.Fatalf("unexpected interrupt_only min query length: %d, %v", got, err)
	}
	if got, err := minQueryLengthForProbePath(model.ProbePathPassive); err != nil || got != 1 {
		t.Fatalf("unexpected passive min query length: %d, %v", got, err)
	}
	if _, err := minQueryLengthForProbePath(model.ProbePath("bad")); err == nil {
		t.Fatal("expected invalid probe path error")
	}

	if err := validateByteField("field", 256); err == nil {
		t.Fatal("expected validateByteField to reject >255")
	}
	if err := validateByteField("field", 255); err != nil {
		t.Fatalf("validateByteField(255) error = %v", err)
	}
}

func TestSelectorAndSubsetNegativeCases(t *testing.T) {
	selector := EndpointSelector{
		InterfaceNumbers:         []int{3},
		RequiredInputReportIDs:   []int{0x54},
		RequiredFeatureReportIDs: []int{0x51},
	}

	if selector.Matches(model.HidCandidate{}) {
		t.Fatal("expected selector mismatch when interface number is missing")
	}
	if selector.Matches(model.HidCandidate{
		InterfaceNumber:  intPtr(2),
		InputReportIDs:   []int{0x54},
		FeatureReportIDs: []int{0x51},
	}) {
		t.Fatal("expected selector mismatch for wrong interface")
	}
	if IsSubsetInt([]int{1, 2}, []int{1}) {
		t.Fatal("expected IsSubsetInt to reject missing values")
	}
}

func TestProfileSourceFallback(t *testing.T) {
	if got := profileSource(ProfileSpec{ID: "m7"}); got != `profile "m7"` {
		t.Fatalf("unexpected fallback source %q", got)
	}
}

func TestProfileRegistryDebugOutput(t *testing.T) {
	registry, err := BuildProfileRegistry([]ProfileSpec{
		{ID: "alpha", Name: "Alpha", SourcePath: "profiles/alpha.yaml"},
	})
	if err != nil {
		t.Fatalf("BuildProfileRegistry() error = %v", err)
	}

	debug := registry.DebugOutput()
	if len(debug) != 1 || debug[0].ID != "alpha" {
		t.Fatalf("unexpected registry debug output %#v", debug)
	}
}

func TestProfileDebugOutputNormalizesFields(t *testing.T) {
	spec := validSpec()
	spec.ID = "M7"
	spec.ChargingStatusBytes = []int{3, 1}
	spec.QueryEndpoint.RequiredFeatureReportIDs = []int{81, 5}
	spec.QueryEndpoint.RequiredInputReportIDs = []int{9, 1}
	spec.WakeEndpoint.InterfaceNumbers = []int{3, 1}

	output := spec.DebugOutput()

	if output.ID != "m7" {
		t.Fatalf("unexpected normalized id %q", output.ID)
	}
	if output.VendorID != "3434" {
		t.Fatalf("unexpected normalized vendor id %q", output.VendorID)
	}
	if output.PrimeQueryCmd == nil || *output.PrimeQueryCmd != 7 {
		t.Fatalf("unexpected prime_query_cmd %#v", output.PrimeQueryCmd)
	}
	if !reflect.DeepEqual(output.ChargingStatusBytes, []int{1, 3}) {
		t.Fatalf("unexpected charging_status_bytes %#v", output.ChargingStatusBytes)
	}
	if !reflect.DeepEqual(output.QueryEndpoint.RequiredFeatureReportIDs, []int{5, 81}) {
		t.Fatalf("unexpected query feature ids %#v", output.QueryEndpoint.RequiredFeatureReportIDs)
	}
	if !reflect.DeepEqual(output.QueryEndpoint.RequiredInputReportIDs, []int{1, 9}) {
		t.Fatalf("unexpected query input ids %#v", output.QueryEndpoint.RequiredInputReportIDs)
	}
	if !reflect.DeepEqual(output.WakeEndpoint.InterfaceNumbers, []int{1, 3}) {
		t.Fatalf("unexpected wake interface numbers %#v", output.WakeEndpoint.InterfaceNumbers)
	}
	if output.ExpectedSignature != "343444d0" {
		t.Fatalf("unexpected expected_signature %q", output.ExpectedSignature)
	}
}

func validSpec() ProfileSpec {
	prime := 7
	return ProfileSpec{
		ID:                       "m7",
		Name:                     "Keychron M7",
		SourcePath:               "profiles/keychron_m7.yaml",
		VendorID:                 "3434",
		TransportProductIDs:      map[model.Transport]string{model.TransportUSBDirect: "d044", model.TransportReceiver: "d030"},
		WakeReport:               []byte{0x00, 0xB2},
		QueryReportID:            0x51,
		PrimeQueryCmd:            &prime,
		BatteryQueryCmd:          0x06,
		QueryLength:              21,
		ExpectedSignature:        []byte{0x34, 0x34, 0x44, 0xd0},
		BatteryOffset:            11,
		StatusOffset:             12,
		ProbePath:                model.ProbePathFeatureOrInterrupt,
		FallbackInputReportID:    0x54,
		FallbackInputCmd:         0xE4,
		FallbackInputLength:      21,
		FallbackBatteryOffset:    2,
		FallbackBatteryBucketMax: 4,
		DeviceType:               "mouse",
		IconName:                 "input-mouse",
		ChargingStatusBytes:      []int{1},
		QueryEndpoint: EndpointSelector{
			RequiredFeatureReportIDs: []int{0x51},
		},
		WakeEndpoint: EndpointSelector{
			InterfaceNumbers: []int{3},
		},
	}
}

func strPtr(v string) *string {
	return &v
}
