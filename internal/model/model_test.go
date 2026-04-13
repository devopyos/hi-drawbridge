//go:build linux

package model_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/probe"
)

func TestParseTransport(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		want      model.Transport
		wantValid bool
		wantErr   string
	}{
		{
			name:      "usb direct",
			value:     "usb_direct",
			want:      model.TransportUSBDirect,
			wantValid: true,
		},
		{
			name:      "trimmed receiver",
			value:     " Receiver ",
			want:      model.TransportReceiver,
			wantValid: true,
		},
		{
			name:    "invalid",
			value:   "bluetooth",
			wantErr: `unknown transport "bluetooth"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParseTransport(tt.value)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error")
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseTransport(%q) = %q, want %q", tt.value, got, tt.want)
			}
			if got.IsValid() != tt.wantValid {
				t.Fatalf("transport %q valid = %t, want %t", got, got.IsValid(), tt.wantValid)
			}
		})
	}
}

func TestParseProbePath(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    model.ProbePath
		wantErr string
	}{
		{
			name:  "feature only",
			value: "feature_only",
			want:  model.ProbePathFeatureOnly,
		},
		{
			name:  "trimmed passive",
			value: " Passive ",
			want:  model.ProbePathPassive,
		},
		{
			name:    "invalid",
			value:   "something_else",
			wantErr: `invalid probe_path "something_else"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParseProbePath(tt.value)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error")
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseProbePath(%q) = %q, want %q", tt.value, got, tt.want)
			}
			if !got.IsValid() {
				t.Fatalf("expected %q to be valid", got)
			}
		})
	}
}

func TestParseFrameSource(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    model.FrameSource
		wantErr string
	}{
		{
			name:  "feature",
			value: "feature",
			want:  model.FrameSourceFeature,
		},
		{
			name:  "trimmed interrupt",
			value: " Interrupt ",
			want:  model.FrameSourceInterrupt,
		},
		{
			name:    "invalid",
			value:   "serial",
			wantErr: `invalid frame_source "serial"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParseFrameSource(tt.value)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error")
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseFrameSource(%q) = %q, want %q", tt.value, got, tt.want)
			}
			if !got.IsValid() {
				t.Fatalf("expected %q to be valid", got)
			}
		})
	}
}

func TestHidCandidateMarshalJSONSortsAndNormalizesSlices(t *testing.T) {
	candidate := model.HidCandidate{
		HidrawName:       "hidraw0",
		Path:             "/dev/hidraw0",
		SysfsPath:        "/sys/class/hidraw/hidraw0",
		Transport:        model.TransportUSBDirect,
		VendorID:         "3434",
		ProductID:        "d044",
		HidName:          "Keychron M7",
		StableDeviceID:   "device-1",
		InputReportIDs:   []int{0x54, 0x51},
		FeatureReportIDs: nil,
	}

	data, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	fields := jsonFields(t, data)
	if string(fields["input_report_ids"]) != `[81,84]` {
		t.Fatalf("expected sorted input_report_ids, got %s", fields["input_report_ids"])
	}
	if string(fields["feature_report_ids"]) != `[]` {
		t.Fatalf("expected normalized empty feature_report_ids, got %s", fields["feature_report_ids"])
	}
	if candidate.InputReportIDs[0] != 0x54 || candidate.InputReportIDs[1] != 0x51 {
		t.Fatalf("expected original candidate slice to remain unchanged, got %v", candidate.InputReportIDs)
	}
}

func TestProbeResultMarshalJSONIncludesStructuredErrorCodes(t *testing.T) {
	result := model.ProbeResult{
		CandidatePath: "/dev/hidraw0",
		Error:         &probe.FeatureProbeError{Code: probe.FeatureProbeErrorInterruptFrameInvalid},
		WakeError:     errors.New("wake failed"),
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	fields := jsonFields(t, data)
	if string(fields["error"]) != `"interrupt_frame_invalid"` {
		t.Fatalf("expected error message field, got %s", fields["error"])
	}
	if string(fields["error_code"]) != `"interrupt_frame_invalid"` {
		t.Fatalf("expected error_code field, got %s", fields["error_code"])
	}
	if string(fields["wake_error"]) != `"wake failed"` {
		t.Fatalf("expected wake_error field, got %s", fields["wake_error"])
	}
	if _, ok := fields["wake_error_code"]; ok {
		t.Fatalf("expected wake_error_code to be omitted for generic errors")
	}
}

func TestProbeResultMarshalJSONOmitsCodeForMessageErrors(t *testing.T) {
	result := model.ProbeResult{
		CandidatePath: "/dev/hidraw0",
		Error:         &probe.FeatureProbeError{Code: probe.FeatureProbeErrorMessage, Message: "custom failure"},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	fields := jsonFields(t, data)
	if string(fields["error"]) != `"custom failure"` {
		t.Fatalf("expected error message field, got %s", fields["error"])
	}
	if _, ok := fields["error_code"]; ok {
		t.Fatalf("expected error_code to be omitted for message-only errors")
	}
}

func TestBridgeRunResultMarshalJSONNormalizesEmptySlices(t *testing.T) {
	result := model.BridgeRunResult{
		ProfileID: "m7",
		Source:    "probe",
		Error:     errors.New("probe failed"),
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	fields := jsonFields(t, data)
	for _, key := range []string{
		"devices",
		"candidates",
		"targets",
		"probe_results",
		"best_readings",
		"discovery_diagnostics",
	} {
		if string(fields[key]) != `[]` {
			t.Fatalf("expected %s to marshal as [], got %s", key, fields[key])
		}
	}
	if string(fields["error"]) != `"probe failed"` {
		t.Fatalf("expected error field, got %s", fields["error"])
	}
}

func TestMultiBridgeRunResultMarshalJSONNormalizesEmptySlices(t *testing.T) {
	result := model.MultiBridgeRunResult{}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	fields := jsonFields(t, data)
	if string(fields["devices"]) != `[]` {
		t.Fatalf("expected devices to marshal as [], got %s", fields["devices"])
	}
	if string(fields["profile_results"]) != `[]` {
		t.Fatalf("expected profile_results to marshal as [], got %s", fields["profile_results"])
	}
}

func TestHexEncode(t *testing.T) {
	if got := model.HexEncode([]byte{0x34, 0x44, 0xD0}); got != "3444d0" {
		t.Fatalf("HexEncode() = %q, want %q", got, "3444d0")
	}
}

func jsonFields(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	return fields
}
