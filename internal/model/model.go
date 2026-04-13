//go:build linux

package model

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrNoCandidates = errors.New("no HID candidates discovered")

// Transport indicates how a HID device is connected to the host.
type Transport string

const (
	// TransportUSBDirect represents a device connected directly via USB.
	TransportUSBDirect Transport = "usb_direct"
	// TransportReceiver represents a device connected through a wireless receiver dongle.
	TransportReceiver Transport = "receiver"
)

// IsValid reports whether t is a known transport value.
func (t Transport) IsValid() bool {
	switch t {
	case TransportUSBDirect, TransportReceiver:
		return true
	default:
		return false
	}
}

// ParseTransport parses a user-provided transport string.
func ParseTransport(value string) (Transport, error) {
	transport := Transport(strings.ToLower(strings.TrimSpace(value)))
	if !transport.IsValid() {
		return "", fmt.Errorf("unknown transport %q", value)
	}

	return transport, nil
}

// ProbePath selects the HID report path used to query battery status.
type ProbePath string

const (
	// ProbePathFeatureOrInterrupt tries feature reports first, then falls back to interrupt reads.
	ProbePathFeatureOrInterrupt ProbePath = "feature_or_interrupt"
	// ProbePathFeatureOnly uses only HID feature reports for battery queries.
	ProbePathFeatureOnly ProbePath = "feature_only"
	// ProbePathInterruptOnly sends an output report and reads the interrupt endpoint.
	ProbePathInterruptOnly ProbePath = "interrupt_only"
	// ProbePathPassive passively listens for interrupt frames without sending any report.
	ProbePathPassive ProbePath = "passive"
)

// IsValid reports whether p is a known probe path value.
func (p ProbePath) IsValid() bool {
	switch p {
	case ProbePathFeatureOrInterrupt, ProbePathFeatureOnly, ProbePathInterruptOnly, ProbePathPassive:
		return true
	default:
		return false
	}
}

// ParseProbePath parses a user-provided probe path string.
func ParseProbePath(value string) (ProbePath, error) {
	path := ProbePath(strings.ToLower(strings.TrimSpace(value)))
	if !path.IsValid() {
		return "", fmt.Errorf("invalid probe_path %q", value)
	}

	return path, nil
}

// FrameSource identifies which HID endpoint produced a battery reading.
type FrameSource string

const (
	// FrameSourceFeature indicates the reading came from a feature report.
	FrameSourceFeature FrameSource = "feature"
	// FrameSourceInterrupt indicates the reading came from an interrupt endpoint.
	FrameSourceInterrupt FrameSource = "interrupt"
)

// IsValid reports whether s is a known frame source value.
func (s FrameSource) IsValid() bool {
	switch s {
	case FrameSourceFeature, FrameSourceInterrupt:
		return true
	default:
		return false
	}
}

// ParseFrameSource parses a frame source string.
func ParseFrameSource(value string) (FrameSource, error) {
	source := FrameSource(strings.ToLower(strings.TrimSpace(value)))
	if !source.IsValid() {
		return "", fmt.Errorf("invalid frame_source %q", value)
	}

	return source, nil
}

// ErrorCoder exposes a machine-readable error code for JSON/debug output.
type ErrorCoder interface {
	error
	ErrorCode() string
}

// BatteryDevice represents a device with battery information exposed over D-Bus.
type BatteryDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DeviceType string `json:"device_type"`
	IconName   string `json:"icon_name"`
	Percentage int    `json:"percentage"`
	IsCharging bool   `json:"is_charging"`
}

// HidCandidate describes a discovered hidraw device that may support battery queries.
type HidCandidate struct {
	HidrawName       string    `json:"hidraw_name"`
	Path             string    `json:"path"`
	SysfsPath        string    `json:"sysfs_path"`
	Transport        Transport `json:"transport"`
	InterfaceNumber  *int      `json:"interface_number,omitempty"`
	VendorID         string    `json:"vendor_id"`
	ProductID        string    `json:"product_id"`
	HidName          string    `json:"hid_name"`
	HidPhys          *string   `json:"hid_phys,omitempty"`
	StableDeviceID   string    `json:"stable_device_id"`
	InputReportIDs   []int     `json:"input_report_ids"`
	FeatureReportIDs []int     `json:"feature_report_ids"`
}

func (c HidCandidate) MarshalJSON() ([]byte, error) {
	type Alias HidCandidate
	sorted := c
	sorted.InputReportIDs = sortedIntsCopy(c.InputReportIDs)
	slices.Sort(sorted.InputReportIDs)
	sorted.FeatureReportIDs = sortedIntsCopy(c.FeatureReportIDs)
	slices.Sort(sorted.FeatureReportIDs)
	return json.Marshal(Alias(sorted))
}

// ProbeTarget pairs query and wake hidraw candidates for a single device probe.
type ProbeTarget struct {
	DeviceID       string       `json:"device_id"`
	Transport      Transport    `json:"transport"`
	QueryCandidate HidCandidate `json:"query_candidate"`
	WakeCandidate  HidCandidate `json:"wake_candidate"`
}

// BatteryReading holds a single battery measurement extracted from a HID report.
type BatteryReading struct {
	DeviceID      string      `json:"device_id"`
	Name          string      `json:"name"`
	Percentage    int         `json:"percentage"`
	Transport     Transport   `json:"transport"`
	ProfileID     string      `json:"profile_id"`
	DeviceType    string      `json:"device_type"`
	IconName      string      `json:"icon_name"`
	Source        FrameSource `json:"source"`
	CandidatePath string      `json:"candidate_path"`
	Status        *int        `json:"status,omitempty"`
}

// ProbeResult captures the outcome of probing a single hidraw candidate for battery data.
type ProbeResult struct {
	CandidatePath  string          `json:"candidate_path"`
	WakePath       *string         `json:"wake_path,omitempty"`
	HidrawName     string          `json:"hidraw_name"`
	StableDeviceID string          `json:"stable_device_id"`
	Transport      Transport       `json:"transport"`
	Success        bool            `json:"success"`
	Attempts       int             `json:"attempts"`
	FrameSource    *string         `json:"frame_source"`
	Error          error           `json:"-"`
	WakeError      error           `json:"-"`
	LastFrameHex   *string         `json:"last_frame_hex"`
	Reading        *BatteryReading `json:"reading"`
}

func (r ProbeResult) MarshalJSON() ([]byte, error) {
	type Alias ProbeResult
	return json.Marshal(&struct {
		Alias
		Error         string `json:"error,omitempty"`
		ErrorCode     string `json:"error_code,omitempty"`
		WakeError     string `json:"wake_error,omitempty"`
		WakeErrorCode string `json:"wake_error_code,omitempty"`
	}{
		Alias:         Alias(r),
		Error:         errToStr(r.Error),
		ErrorCode:     errCode(r.Error),
		WakeError:     errToStr(r.WakeError),
		WakeErrorCode: errCode(r.WakeError),
	})
}

// DiscoveryDiagnostic records an error encountered while scanning a sysfs hidraw entry.
type DiscoveryDiagnostic struct {
	EntryPath string `json:"entry_path"`
	Error     error  `json:"-"`
}

func (d DiscoveryDiagnostic) MarshalJSON() ([]byte, error) {
	type Alias DiscoveryDiagnostic
	return json.Marshal(&struct {
		Alias
		Error string `json:"error,omitempty"`
	}{
		Alias: Alias(d),
		Error: errToStr(d.Error),
	})
}

// BridgeRunResult captures the outcome of a single profile's probe cycle.
type BridgeRunResult struct {
	ProfileID            string                `json:"profile_id"`
	Source               string                `json:"source"`
	Devices              []BatteryDevice       `json:"devices"`
	Candidates           []HidCandidate        `json:"candidates"`
	Targets              []ProbeTarget         `json:"targets"`
	ProbeResults         []ProbeResult         `json:"probe_results"`
	BestReadings         []BatteryReading      `json:"best_readings"`
	DiscoveryDiagnostics []DiscoveryDiagnostic `json:"discovery_diagnostics"`
	UsedStale            bool                  `json:"used_stale"`
	Error                error                 `json:"-"`
}

func (r BridgeRunResult) MarshalJSON() ([]byte, error) {
	type Alias BridgeRunResult
	normalized := r
	normalized.Devices = append([]BatteryDevice{}, r.Devices...)
	normalized.Candidates = append([]HidCandidate{}, r.Candidates...)
	normalized.Targets = append([]ProbeTarget{}, r.Targets...)
	normalized.ProbeResults = append([]ProbeResult{}, r.ProbeResults...)
	normalized.BestReadings = append([]BatteryReading{}, r.BestReadings...)
	normalized.DiscoveryDiagnostics = append([]DiscoveryDiagnostic{}, r.DiscoveryDiagnostics...)
	return json.Marshal(&struct {
		Alias
		Error string `json:"error,omitempty"`
	}{
		Alias: Alias(normalized),
		Error: errToStr(r.Error),
	})
}

// MultiBridgeRunResult aggregates results across multiple profile probe cycles.
type MultiBridgeRunResult struct {
	Devices        []BatteryDevice   `json:"devices"`
	ProfileResults []BridgeRunResult `json:"profile_results"`
}

func (r MultiBridgeRunResult) MarshalJSON() ([]byte, error) {
	type Alias MultiBridgeRunResult
	normalized := r
	normalized.Devices = append([]BatteryDevice{}, r.Devices...)
	normalized.ProfileResults = append([]BridgeRunResult{}, r.ProfileResults...)
	return json.Marshal(Alias(normalized))
}

// HexEncode returns the lowercase hexadecimal encoding of b.
func HexEncode(b []byte) string {
	return hex.EncodeToString(b)
}

func sortedIntsCopy(values []int) []int {
	return append([]int{}, values...)
}

func errToStr(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func errCode(err error) string {
	var coder ErrorCoder
	if errors.As(err, &coder) {
		return coder.ErrorCode()
	}

	return ""
}
