//go:build linux

package profile

import (
	"bytes"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

const (
	maxCatalogProfileFileBytes int64 = 1 << 20
	maxQueryLength                   = 256
	maxWakeReportBytes               = 256
	maxExpectedSignatureBytes        = 64
)

//go:embed profiles/*.yaml
var embeddedProfilesFS embed.FS

var (
	profileCatalogFS  fs.FS = embeddedProfilesFS
	profileCatalogDir       = "profiles"
)

type yamlEndpointSelector struct {
	InterfaceNumbers         []int `yaml:"interface_numbers,omitempty"`
	RequiredInputReportIDs   []int `yaml:"required_input_report_ids,omitempty"`
	RequiredFeatureReportIDs []int `yaml:"required_feature_report_ids,omitempty"`
}

func (y *yamlEndpointSelector) toSelector() EndpointSelector {
	return EndpointSelector{
		InterfaceNumbers:         y.InterfaceNumbers,
		RequiredInputReportIDs:   y.RequiredInputReportIDs,
		RequiredFeatureReportIDs: y.RequiredFeatureReportIDs,
	}
}

type yamlProfile struct {
	ID                       *string               `yaml:"id"`
	Name                     *string               `yaml:"name"`
	VendorID                 *string               `yaml:"vendor_id"`
	TransportProductIDs      *map[string]string    `yaml:"transport_product_ids"`
	WakeReportHex            *string               `yaml:"wake_report_hex"`
	QueryReportID            *int                  `yaml:"query_report_id"`
	PrimeQueryCmd            *int                  `yaml:"prime_query_cmd,omitempty"`
	BatteryQueryCmd          *int                  `yaml:"battery_query_cmd"`
	QueryLength              *int                  `yaml:"query_length"`
	ExpectedSignatureHex     *string               `yaml:"expected_signature_hex"`
	BatteryOffset            *int                  `yaml:"battery_offset"`
	StatusOffset             *int                  `yaml:"status_offset"`
	ProbePath                *string               `yaml:"probe_path"`
	FallbackInputReportID    *int                  `yaml:"fallback_input_report_id"`
	FallbackInputCmd         *int                  `yaml:"fallback_input_cmd"`
	FallbackInputLength      *int                  `yaml:"fallback_input_length"`
	FallbackBatteryOffset    *int                  `yaml:"fallback_battery_offset"`
	FallbackBatteryBucketMax *int                  `yaml:"fallback_battery_bucket_max"`
	DeviceType               *string               `yaml:"device_type"`
	IconName                 *string               `yaml:"icon_name"`
	ChargingStatusBytes      []int                 `yaml:"charging_status_bytes,omitempty"`
	QueryEndpoint            *yamlEndpointSelector `yaml:"query_endpoint"`
	WakeEndpoint             *yamlEndpointSelector `yaml:"wake_endpoint,omitempty"`
}

type requiredProfileFields struct {
	ID                       string
	Name                     string
	VendorID                 string
	TransportProductIDs      map[model.Transport]string
	WakeReport               []byte
	QueryReportID            int
	BatteryQueryCmd          int
	QueryLength              int
	ExpectedSignature        []byte
	BatteryOffset            int
	StatusOffset             int
	ProbePath                string
	FallbackInputReportID    int
	FallbackInputCmd         int
	FallbackInputLength      int
	FallbackBatteryOffset    int
	FallbackBatteryBucketMax int
	DeviceType               string
	IconName                 string
	QueryEndpoint            yamlEndpointSelector
	WakeEndpoint             EndpointSelector
}

type fieldExtractor struct {
	err error
}

// LoadEmbeddedProfiles reads shipped profile YAML files from the embedded catalog.
func LoadEmbeddedProfiles() ([]ProfileSpec, error) {
	return loadProfilesFS(profileCatalogFS, profileCatalogDir)
}

// LoadProfileRegistry loads the embedded catalog and constructs a validated registry.
func LoadProfileRegistry() (*ProfileRegistry, error) {
	profiles, err := LoadEmbeddedProfiles()
	if err != nil {
		return nil, err
	}

	return BuildProfileRegistry(profiles)
}

// LoadProfileRegistryWithOverlayDir loads the embedded catalog and overlays local YAML profiles from dir.
func LoadProfileRegistryWithOverlayDir(dir string, allowMissing bool) (*ProfileRegistry, bool, error) {
	baseProfiles, err := LoadEmbeddedProfiles()
	if err != nil {
		return nil, false, err
	}

	overlayProfiles, exists, err := loadOverlayProfilesDir(dir, allowMissing)
	if err != nil {
		return nil, false, err
	}

	mergedProfiles, err := mergeProfiles(baseProfiles, overlayProfiles)
	if err != nil {
		return nil, false, err
	}

	registry, err := BuildProfileRegistry(mergedProfiles)
	if err != nil {
		return nil, false, err
	}

	return registry, exists, nil
}

func loadProfilesFS(fsys fs.FS, dir string) ([]ProfileSpec, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("load profile catalog %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("profile catalog %s is empty", dir)
	}

	profiles := make([]ProfileSpec, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if path.Ext(entry.Name()) != ".yaml" {
			continue
		}

		filePath := path.Join(dir, entry.Name())
		data, err := readProfileFile(fsys, filePath)
		if err != nil {
			return nil, err
		}

		spec, err := decodeProfileYAML(filePath, data)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, spec)
	}

	if len(profiles) == 0 {
		return nil, fmt.Errorf("profile catalog %s contains no .yaml files", dir)
	}

	return profiles, nil
}

func readProfileFile(fsys fs.FS, filePath string) ([]byte, error) {
	return readProfileFileWithDisplayPath(fsys, filePath, filePath)
}

func readProfileFileWithDisplayPath(fsys fs.FS, filePath, displayPath string) ([]byte, error) {
	info, err := fs.Stat(fsys, filePath)
	if err != nil {
		return nil, fmt.Errorf("stat profile file %s: %w", displayPath, err)
	}
	if info.Size() > maxCatalogProfileFileBytes {
		return nil, fmt.Errorf("profile file %s exceeds max size %d bytes", displayPath, maxCatalogProfileFileBytes)
	}

	data, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return nil, fmt.Errorf("read profile file %s: %w", displayPath, err)
	}

	return data, nil
}

func loadOverlayProfilesDir(dir string, allowMissing bool) ([]ProfileSpec, bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) && allowMissing {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("stat profile overlay directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, true, fmt.Errorf("profile overlay path %s must be a directory", dir)
	}

	fsys := os.DirFS(dir)
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, true, fmt.Errorf("load profile overlay directory %s: %w", dir, err)
	}

	profiles := make([]ProfileSpec, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if path.Ext(entry.Name()) != ".yaml" {
			continue
		}

		sourcePath := filepath.Join(dir, entry.Name())
		data, err := readProfileFileWithDisplayPath(fsys, entry.Name(), sourcePath)
		if err != nil {
			return nil, true, err
		}

		spec, err := decodeProfileYAML(sourcePath, data)
		if err != nil {
			return nil, true, err
		}
		profiles = append(profiles, spec)
	}

	return profiles, true, nil
}

func mergeProfiles(baseProfiles, overlayProfiles []ProfileSpec) ([]ProfileSpec, error) {
	mergedProfiles := make([]ProfileSpec, len(baseProfiles))
	copy(mergedProfiles, baseProfiles)

	baseIndexByID := make(map[string]int, len(baseProfiles))
	for i, profileSpec := range mergedProfiles {
		baseIndexByID[strings.ToLower(strings.TrimSpace(profileSpec.ID))] = i
	}

	overlayByID := make(map[string]ProfileSpec, len(overlayProfiles))
	for _, profileSpec := range overlayProfiles {
		id := strings.ToLower(strings.TrimSpace(profileSpec.ID))
		if existing, ok := overlayByID[id]; ok {
			return nil, fmt.Errorf(
				"duplicate profile id %q in %s and %s",
				id,
				profileSource(existing),
				profileSource(profileSpec),
			)
		}
		overlayByID[id] = profileSpec

		if index, ok := baseIndexByID[id]; ok {
			mergedProfiles[index] = profileSpec
			continue
		}

		mergedProfiles = append(mergedProfiles, profileSpec)
	}

	return mergedProfiles, nil
}

func decodeProfileYAML(filePath string, data []byte) (ProfileSpec, error) {
	var yp yamlProfile

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	decodeErr := decoder.Decode(&yp)
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		return ProfileSpec{}, fmt.Errorf("parse profile file %s: %w", filePath, decodeErr)
	}

	if decodeErr == nil {
		var trailingDoc any
		trailingErr := decoder.Decode(&trailingDoc)
		if trailingErr == nil {
			return ProfileSpec{}, fmt.Errorf("parse profile file %s: multiple YAML documents are not supported", filePath)
		}
		if !errors.Is(trailingErr, io.EOF) {
			return ProfileSpec{}, fmt.Errorf("parse profile file %s: %w", filePath, trailingErr)
		}
	}

	return convertProfile(filePath, &yp)
}

func convertProfile(sourcePath string, yp *yamlProfile) (ProfileSpec, error) {
	fields, err := extractProfileFields(yp)
	if err != nil {
		return ProfileSpec{}, err
	}

	probePath, err := model.ParseProbePath(fields.ProbePath)
	if err != nil {
		return ProfileSpec{}, err
	}

	p := ProfileSpec{
		ID:                       strings.ToLower(fields.ID),
		Name:                     fields.Name,
		SourcePath:               sourcePath,
		VendorID:                 fields.VendorID,
		TransportProductIDs:      fields.TransportProductIDs,
		WakeReport:               fields.WakeReport,
		QueryReportID:            fields.QueryReportID,
		PrimeQueryCmd:            yp.PrimeQueryCmd,
		BatteryQueryCmd:          fields.BatteryQueryCmd,
		QueryLength:              fields.QueryLength,
		ExpectedSignature:        fields.ExpectedSignature,
		BatteryOffset:            fields.BatteryOffset,
		StatusOffset:             fields.StatusOffset,
		ProbePath:                probePath,
		FallbackInputReportID:    fields.FallbackInputReportID,
		FallbackInputCmd:         fields.FallbackInputCmd,
		FallbackInputLength:      fields.FallbackInputLength,
		FallbackBatteryOffset:    fields.FallbackBatteryOffset,
		FallbackBatteryBucketMax: fields.FallbackBatteryBucketMax,
		DeviceType:               fields.DeviceType,
		IconName:                 fields.IconName,
		ChargingStatusBytes:      yp.ChargingStatusBytes,
		QueryEndpoint:            fields.QueryEndpoint.toSelector(),
		WakeEndpoint:             fields.WakeEndpoint,
	}

	if err := validateProfileSpec(p); err != nil {
		return ProfileSpec{}, fmt.Errorf("%s: %w", sourcePath, err)
	}

	return p, nil
}

func (e *fieldExtractor) String(field string, value *string) string {
	if e.err != nil {
		return ""
	}

	v, err := requireString(field, value)
	if err != nil {
		e.err = err
		return ""
	}

	return v
}

func (e *fieldExtractor) Int(field string, value *int) int {
	if e.err != nil {
		return 0
	}

	v, err := requireInt(field, value)
	if err != nil {
		e.err = err
		return 0
	}

	return v
}

func (e *fieldExtractor) HexID(field string, value *string) string {
	if e.err != nil {
		return ""
	}

	v := e.String(field, value)
	if e.err != nil {
		return ""
	}

	parsed, err := parseHexID(field, v)
	if err != nil {
		e.err = err
		return ""
	}

	return parsed
}

func (e *fieldExtractor) TransportProductIDs(field string, value *map[string]string) map[model.Transport]string {
	if e.err != nil {
		return nil
	}

	raw, err := requireTransportProductIDs(field, value)
	if err != nil {
		e.err = err
		return nil
	}

	parsed, err := parseTransportProductIDs(raw)
	if err != nil {
		e.err = err
		return nil
	}

	return parsed
}

func (e *fieldExtractor) HexBytes(field string, value *string, maxBytes int) []byte {
	if e.err != nil {
		return nil
	}

	decoded, err := decodeRequiredHexBytes(field, value, maxBytes)
	if err != nil {
		e.err = err
		return nil
	}

	return decoded
}

func (e *fieldExtractor) Endpoint(field string, value *yamlEndpointSelector) yamlEndpointSelector {
	if e.err != nil {
		return yamlEndpointSelector{}
	}

	v, err := requireEndpointSelector(field, value)
	if err != nil {
		e.err = err
		return yamlEndpointSelector{}
	}

	return v
}

func extractProfileFields(yp *yamlProfile) (requiredProfileFields, error) {
	extractor := &fieldExtractor{}

	idValue := extractor.String("id", yp.ID)
	name := extractor.String("name", yp.Name)
	vendorID := extractor.HexID("vendor_id", yp.VendorID)
	transportIDs := extractor.TransportProductIDs("transport_product_ids", yp.TransportProductIDs)
	wakeReport := extractor.HexBytes("wake_report_hex", yp.WakeReportHex, maxWakeReportBytes)
	expectedSig := extractor.HexBytes("expected_signature_hex", yp.ExpectedSignatureHex, maxExpectedSignatureBytes)
	queryReportID := extractor.Int("query_report_id", yp.QueryReportID)
	batteryQueryCmd := extractor.Int("battery_query_cmd", yp.BatteryQueryCmd)
	queryLength := extractor.Int("query_length", yp.QueryLength)
	batteryOffset := extractor.Int("battery_offset", yp.BatteryOffset)
	statusOffset := extractor.Int("status_offset", yp.StatusOffset)
	probePathValue := extractor.String("probe_path", yp.ProbePath)
	fallbackInputReportID := extractor.Int("fallback_input_report_id", yp.FallbackInputReportID)
	fallbackInputCmd := extractor.Int("fallback_input_cmd", yp.FallbackInputCmd)
	fallbackInputLength := extractor.Int("fallback_input_length", yp.FallbackInputLength)
	fallbackBatteryOffset := extractor.Int("fallback_battery_offset", yp.FallbackBatteryOffset)
	fallbackBatteryBucketMax := extractor.Int("fallback_battery_bucket_max", yp.FallbackBatteryBucketMax)
	deviceType := extractor.String("device_type", yp.DeviceType)
	iconName := extractor.String("icon_name", yp.IconName)
	queryEndpoint := extractor.Endpoint("query_endpoint", yp.QueryEndpoint)

	if extractor.err != nil {
		return requiredProfileFields{}, extractor.err
	}

	wakeEndpoint := queryEndpoint.toSelector()
	if yp.WakeEndpoint != nil {
		wakeEndpoint = yp.WakeEndpoint.toSelector()
	}

	return requiredProfileFields{
		ID:                       idValue,
		Name:                     name,
		VendorID:                 vendorID,
		TransportProductIDs:      transportIDs,
		WakeReport:               wakeReport,
		QueryReportID:            queryReportID,
		BatteryQueryCmd:          batteryQueryCmd,
		QueryLength:              queryLength,
		ExpectedSignature:        expectedSig,
		BatteryOffset:            batteryOffset,
		StatusOffset:             statusOffset,
		ProbePath:                probePathValue,
		FallbackInputReportID:    fallbackInputReportID,
		FallbackInputCmd:         fallbackInputCmd,
		FallbackInputLength:      fallbackInputLength,
		FallbackBatteryOffset:    fallbackBatteryOffset,
		FallbackBatteryBucketMax: fallbackBatteryBucketMax,
		DeviceType:               deviceType,
		IconName:                 iconName,
		QueryEndpoint:            queryEndpoint,
		WakeEndpoint:             wakeEndpoint,
	}, nil
}

func parseTransportProductIDs(raw map[string]string) (map[model.Transport]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("transport_product_ids must not be empty")
	}

	out := make(map[model.Transport]string, len(raw))
	seen := make(map[string]model.Transport, len(raw))
	for key, value := range raw {
		transport, err := model.ParseTransport(key)
		if err != nil {
			return nil, fmt.Errorf("invalid transport key %q: %w", key, err)
		}

		transportKey := string(transport)
		normalizedID, err := parseHexID("transport_product_ids."+transportKey, value)
		if err != nil {
			return nil, err
		}

		if existing, ok := seen[normalizedID]; ok {
			return nil, fmt.Errorf("transport_product_ids must not reuse product id %q for %s and %s", normalizedID, existing, transportKey)
		}

		seen[normalizedID] = transport
		out[transport] = normalizedID
	}

	return out, nil
}

func requireString(field string, value *string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("%s is required", field)
	}

	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}

	return trimmed, nil
}

func requireInt(field string, value *int) (int, error) {
	if value == nil {
		return 0, fmt.Errorf("%s is required", field)
	}

	return *value, nil
}

func requireTransportProductIDs(field string, value *map[string]string) (map[string]string, error) {
	if value == nil {
		return nil, fmt.Errorf("%s is required", field)
	}
	if len(*value) == 0 {
		return nil, fmt.Errorf("%s must not be empty", field)
	}

	return *value, nil
}

func requireEndpointSelector(field string, value *yamlEndpointSelector) (yamlEndpointSelector, error) {
	if value == nil {
		return yamlEndpointSelector{}, fmt.Errorf("%s is required", field)
	}

	return *value, nil
}

func decodeRequiredHexBytes(field string, raw *string, maxBytes int) ([]byte, error) {
	value, err := requireString(field, raw)
	if err != nil {
		return nil, err
	}

	normalized := normalizeHex(value)
	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("%s must be a valid hex string: %w", field, err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("%s must not be empty", field)
	}
	if len(decoded) > maxBytes {
		return nil, fmt.Errorf("%s must decode to at most %d bytes", field, maxBytes)
	}

	return decoded, nil
}
