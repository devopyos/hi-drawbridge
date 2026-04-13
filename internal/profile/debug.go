//go:build linux

package profile

import (
	"maps"
	"slices"
	"strings"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

// EndpointSelectorDebug is the JSON-ready diagnostic view of an endpoint selector.
type EndpointSelectorDebug struct {
	InterfaceNumbers         []int `json:"interface_numbers"`
	RequiredInputReportIDs   []int `json:"required_input_report_ids"`
	RequiredFeatureReportIDs []int `json:"required_feature_report_ids"`
}

// ProfileDebug is the JSON-ready diagnostic view of a profile specification.
type ProfileDebug struct {
	BatteryOffset            int                        `json:"battery_offset"`
	BatteryQueryCmd          int                        `json:"battery_query_cmd"`
	ChargingStatusBytes      []int                      `json:"charging_status_bytes"`
	DeviceType               string                     `json:"device_type"`
	ExpectedSignature        string                     `json:"expected_signature"`
	FallbackBatteryBucketMax int                        `json:"fallback_battery_bucket_max"`
	FallbackBatteryOffset    int                        `json:"fallback_battery_offset"`
	FallbackInputCmd         int                        `json:"fallback_input_cmd"`
	FallbackInputLength      int                        `json:"fallback_input_length"`
	FallbackInputReportID    int                        `json:"fallback_input_report_id"`
	IconName                 string                     `json:"icon_name"`
	ID                       string                     `json:"id"`
	Name                     string                     `json:"name"`
	PrimeQueryCmd            *int                       `json:"prime_query_cmd,omitempty"`
	ProbePath                model.ProbePath            `json:"probe_path"`
	QueryEndpoint            EndpointSelectorDebug      `json:"query_endpoint"`
	QueryLength              int                        `json:"query_length"`
	QueryReportID            int                        `json:"query_report_id"`
	SourcePath               string                     `json:"source_path"`
	StatusOffset             int                        `json:"status_offset"`
	TransportProductIDs      map[model.Transport]string `json:"transport_product_ids"`
	VendorID                 string                     `json:"vendor_id"`
	WakeEndpoint             EndpointSelectorDebug      `json:"wake_endpoint"`
	WakeReport               string                     `json:"wake_report"`
}

// DebugOutput returns a structured diagnostic view of the selector.
func (s EndpointSelector) DebugOutput() EndpointSelectorDebug {
	var interfaceNumbers []int
	if len(s.InterfaceNumbers) > 0 {
		interfaceNumbers = sortedInts(s.InterfaceNumbers)
	}

	return EndpointSelectorDebug{
		InterfaceNumbers:         interfaceNumbers,
		RequiredInputReportIDs:   sortedInts(s.RequiredInputReportIDs),
		RequiredFeatureReportIDs: sortedInts(s.RequiredFeatureReportIDs),
	}
}

// DebugOutput returns a structured diagnostic view of the profile.
func (p ProfileSpec) DebugOutput() ProfileDebug {
	output := ProfileDebug{
		BatteryOffset:            p.BatteryOffset,
		BatteryQueryCmd:          p.BatteryQueryCmd,
		ChargingStatusBytes:      sortedInts(p.ChargingStatusBytes),
		DeviceType:               p.DeviceType,
		ExpectedSignature:        model.HexEncode(p.ExpectedSignature),
		FallbackBatteryBucketMax: p.FallbackBatteryBucketMax,
		FallbackBatteryOffset:    p.FallbackBatteryOffset,
		FallbackInputCmd:         p.FallbackInputCmd,
		FallbackInputLength:      p.FallbackInputLength,
		FallbackInputReportID:    p.FallbackInputReportID,
		IconName:                 p.IconName,
		ID:                       strings.ToLower(p.ID),
		Name:                     p.Name,
		ProbePath:                p.ProbePath,
		QueryEndpoint:            p.QueryEndpoint.DebugOutput(),
		QueryLength:              p.QueryLength,
		QueryReportID:            p.QueryReportID,
		SourcePath:               p.SourcePath,
		StatusOffset:             p.StatusOffset,
		TransportProductIDs:      maps.Clone(p.TransportProductIDs),
		VendorID:                 strings.ToLower(p.VendorID),
		WakeEndpoint:             p.WakeEndpoint.DebugOutput(),
		WakeReport:               model.HexEncode(p.WakeReport),
	}

	if p.PrimeQueryCmd != nil {
		value := *p.PrimeQueryCmd
		output.PrimeQueryCmd = &value
	}

	return output
}

// DebugOutput returns structured diagnostic views of all profiles in registry order.
func (r *ProfileRegistry) DebugOutput() []ProfileDebug {
	result := make([]ProfileDebug, len(r.Profiles))
	for i, p := range r.Profiles {
		result[i] = p.DebugOutput()
	}

	return result
}

func sortedInts(values []int) []int {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}
