//go:build linux

package profile

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

func validateProfileSpec(p ProfileSpec) error {
	if err := validateProfileRequiredFields(p); err != nil {
		return err
	}
	if err := validateProfileOffsetFields(p); err != nil {
		return err
	}
	if err := validateProfileByteFields(p); err != nil {
		return err
	}
	if err := validateProfileProbePathFields(p); err != nil {
		return err
	}

	return validateProfileSelectorFields(p)
}

func validateProfileRequiredFields(p ProfileSpec) error {
	if p.Name == "" {
		return errors.New("name must not be empty")
	}
	if p.DeviceType == "" {
		return errors.New("device_type must not be empty")
	}
	if p.IconName == "" {
		return errors.New("icon_name must not be empty")
	}
	if len(p.WakeReport) == 0 {
		return errors.New("wake_report_hex must not be empty")
	}
	if len(p.WakeReport) > maxWakeReportBytes {
		return fmt.Errorf("wake_report_hex must decode to at most %d bytes", maxWakeReportBytes)
	}
	if len(p.ExpectedSignature) == 0 {
		return errors.New("expected_signature_hex must not be empty")
	}
	if len(p.ExpectedSignature) > maxExpectedSignatureBytes {
		return fmt.Errorf("expected_signature_hex must decode to at most %d bytes", maxExpectedSignatureBytes)
	}

	return nil
}

func validateProfileOffsetFields(p ProfileSpec) error {
	if p.BatteryOffset < 0 {
		return errors.New("battery_offset must be >= 0")
	}
	if p.StatusOffset < 0 {
		return errors.New("status_offset must be >= 0")
	}
	if p.FallbackBatteryOffset < 0 {
		return errors.New("fallback_battery_offset must be >= 0")
	}
	if p.FallbackInputLength < 0 {
		return errors.New("fallback_input_length must be >= 0")
	}
	if p.FallbackBatteryBucketMax < 0 {
		return errors.New("fallback_battery_bucket_max must be >= 0")
	}

	return nil
}

func validateProfileByteFields(p ProfileSpec) error {
	if err := validateByteField("query_report_id", p.QueryReportID); err != nil {
		return err
	}
	if err := validateByteField("battery_query_cmd", p.BatteryQueryCmd); err != nil {
		return err
	}
	if p.PrimeQueryCmd != nil {
		if err := validateByteField("prime_query_cmd", *p.PrimeQueryCmd); err != nil {
			return err
		}
	}
	if err := validateByteField("fallback_input_report_id", p.FallbackInputReportID); err != nil {
		return err
	}
	if err := validateByteField("fallback_input_cmd", p.FallbackInputCmd); err != nil {
		return err
	}
	for idx, value := range p.ChargingStatusBytes {
		if err := validateByteField(fmt.Sprintf("charging_status_bytes[%d]", idx), value); err != nil {
			return err
		}
	}

	return nil
}

func validateProfileSelectorFields(p ProfileSpec) error {
	if err := validateEndpointSelector("query_endpoint", p.QueryEndpoint); err != nil {
		return err
	}

	return validateEndpointSelector("wake_endpoint", p.WakeEndpoint)
}

func validateProfileProbePathFields(p ProfileSpec) error {
	minQueryLength, err := minQueryLengthForProbePath(p.ProbePath)
	if err != nil {
		return err
	}
	if p.QueryLength < minQueryLength {
		return fmt.Errorf("query_length must be >= %d for probe_path %q", minQueryLength, p.ProbePath)
	}
	if p.QueryLength > maxQueryLength {
		return fmt.Errorf("query_length must be <= %d", maxQueryLength)
	}
	if len(p.ExpectedSignature) > p.QueryLength {
		return errors.New("expected_signature_hex must fit inside query_length")
	}

	if p.ProbePath == model.ProbePathFeatureOnly || p.ProbePath == model.ProbePathFeatureOrInterrupt {
		if p.BatteryOffset >= p.QueryLength {
			return errors.New("battery_offset must be < query_length")
		}
		if p.StatusOffset >= p.QueryLength {
			return errors.New("status_offset must be < query_length")
		}
	}

	if p.ProbePath != model.ProbePathFeatureOrInterrupt &&
		p.ProbePath != model.ProbePathInterruptOnly &&
		p.ProbePath != model.ProbePathPassive {
		return nil
	}

	if p.FallbackInputLength < 2 {
		return fmt.Errorf("fallback_input_length must be >= 2 for probe_path %q", p.ProbePath)
	}
	if p.FallbackBatteryOffset >= p.FallbackInputLength {
		return errors.New("fallback_battery_offset must be < fallback_input_length")
	}

	return nil
}

func validateEndpointSelector(field string, selector EndpointSelector) error {
	for idx, value := range selector.InterfaceNumbers {
		if value < 0 {
			return fmt.Errorf("%s.interface_numbers[%d] must be >= 0", field, idx)
		}
	}
	for idx, value := range selector.RequiredInputReportIDs {
		if err := validateByteField(fmt.Sprintf("%s.required_input_report_ids[%d]", field, idx), value); err != nil {
			return err
		}
	}
	for idx, value := range selector.RequiredFeatureReportIDs {
		if err := validateByteField(fmt.Sprintf("%s.required_feature_report_ids[%d]", field, idx), value); err != nil {
			return err
		}
	}

	return nil
}

func minQueryLengthForProbePath(path model.ProbePath) (int, error) {
	switch path {
	case model.ProbePathFeatureOnly, model.ProbePathFeatureOrInterrupt:
		return 2, nil
	case model.ProbePathInterruptOnly:
		return 3, nil
	case model.ProbePathPassive:
		return 1, nil
	default:
		return 0, fmt.Errorf("invalid probe_path %q", path)
	}
}

func validateByteField(field string, value int) error {
	if value < 0 || value > 255 {
		return fmt.Errorf("%s must be in range [0..255]", field)
	}

	return nil
}

func normalizeHexID(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "0x")

	return s
}

func normalizeHex(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ToLower(s), " ", ""))
}

func parseHexID(field, raw string) (string, error) {
	normalized := normalizeHexID(raw)
	if normalized == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}
	if len(normalized) != 4 {
		return "", fmt.Errorf("%s must be a 4-digit hex string", field)
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return "", fmt.Errorf("%s must be a valid hex string: %w", field, err)
	}

	return normalized, nil
}
