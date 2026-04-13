//go:build linux

package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

var (
	inputSuffixRE       = regexp.MustCompile(`/input\d+$`)
	sysfsInputSuffixRE  = regexp.MustCompile(`/input/input\d+$`)
	sysfsHidrawSuffixRE = regexp.MustCompile(`/hidraw/hidraw\d+$`)
	discoveryReadFile   = os.ReadFile
	discoveryStat       = os.Stat
	discoveryEvalLinks  = filepath.EvalSymlinks
	discoveryGlob       = filepath.Glob
	discoverySysfsRoot  = "/sys/class/hidraw"
	discoveryDevRoot    = "/dev"
)

func parseUevent(ueventPath string) (map[string]string, error) {
	data, err := discoveryReadFile(ueventPath)
	if err != nil {
		return nil, err
	}

	entries := make(map[string]string)
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		entries[key] = value
	}

	return entries, nil
}

func parseHidID(hidID string) (vendorID, productID string, err error) {
	parts := strings.Split(hidID, ":")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("invalid HID_ID value: %q", hidID)
	}

	vendorID, err = normalizeHIDIDComponent(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("invalid HID_ID vendor component %q: %w", parts[1], err)
	}

	productID, err = normalizeHIDIDComponent(parts[2])
	if err != nil {
		return "", "", fmt.Errorf("invalid HID_ID product component %q: %w", parts[2], err)
	}

	return vendorID, productID, nil
}

func readInterfaceNumber(deviceLink string) *int {
	ifacePath := filepath.Join(deviceLink, "bInterfaceNumber")
	data, err := discoveryReadFile(ifacePath)
	if err != nil {
		return nil
	}

	val, err := parseHexInt(strings.TrimSpace(string(data)))
	if err != nil {
		return nil
	}

	return &val
}

func parseHexInt(s string) (int, error) {
	val, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		return 0, err
	}

	return int(val), nil
}

func normalizeHIDIDComponent(value string) (string, error) {
	if len(value) < 4 {
		return "", fmt.Errorf("expected at least 4 hex digits, got %q", value)
	}

	normalized := strings.ToLower(value[len(value)-4:])
	if _, err := strconv.ParseUint(normalized, 16, 16); err != nil {
		return "", err
	}

	return normalized, nil
}

// ParseReportDescriptor extracts input and feature report IDs from a raw HID report descriptor.
func ParseReportDescriptor(descriptor []byte) (inputReportIDs, featureReportIDs []int) {
	currentReportID := 0
	offset := 0
	descLen := len(descriptor)

	for offset < descLen {
		prefix := int(descriptor[offset])
		offset++

		if prefix == 0xFE {
			if offset+2 > descLen {
				break
			}

			longItemSize := int(descriptor[offset])
			offset += 2 + longItemSize
			continue
		}

		sizeCode := prefix & 0x03
		dataSize := sizeCode
		if sizeCode == 3 {
			dataSize = 4
		}

		itemType := (prefix >> 2) & 0x03
		itemTag := (prefix >> 4) & 0x0F

		if offset+dataSize > descLen {
			break
		}

		data := descriptor[offset : offset+dataSize]
		offset += dataSize

		switch {
		case itemType == 0x01 && itemTag == 0x08 && len(data) > 0:
			currentReportID = int(data[0])
		case itemType == 0x00 && itemTag == 0x08:
			inputReportIDs = append(inputReportIDs, currentReportID)
		case itemType == 0x00 && itemTag == 0x0B:
			featureReportIDs = append(featureReportIDs, currentReportID)
		}
	}

	return inputReportIDs, featureReportIDs
}

func descriptorSupportsProfile(descriptor []byte, p profile.ProfileSpec) (inputIDs, featureIDs []int, queryMatch, wakeMatch bool) {
	inputIDs, featureIDs = ParseReportDescriptor(descriptor)

	queryMatch = profile.IsSubsetInt(p.QueryEndpoint.RequiredInputReportIDs, inputIDs) &&
		profile.IsSubsetInt(p.QueryEndpoint.RequiredFeatureReportIDs, featureIDs)
	wakeMatch = profile.IsSubsetInt(p.WakeEndpoint.RequiredInputReportIDs, inputIDs) &&
		profile.IsSubsetInt(p.WakeEndpoint.RequiredFeatureReportIDs, featureIDs)

	return inputIDs, featureIDs, queryMatch, wakeMatch
}

func endpointMatchesBestEffort(selector profile.EndpointSelector, iface *int, inputIDs, featureIDs []int) bool {
	if !profile.IsSubsetInt(selector.RequiredInputReportIDs, inputIDs) {
		return false
	}

	if !profile.IsSubsetInt(selector.RequiredFeatureReportIDs, featureIDs) {
		return false
	}

	if len(selector.InterfaceNumbers) == 0 || iface == nil {
		return true
	}

	return slices.Contains(selector.InterfaceNumbers, *iface)
}

func normalizedDeviceSource(hidPhys *string, deviceLink string) string {
	if hidPhys != nil {
		trimmed := strings.TrimSpace(*hidPhys)
		if trimmed != "" {
			return inputSuffixRE.ReplaceAllString(trimmed, "")
		}
	}

	resolved, err := discoveryEvalLinks(deviceLink)
	if err != nil {
		resolved = deviceLink
	}

	withoutInput := sysfsInputSuffixRE.ReplaceAllString(resolved, "")
	withoutHidraw := sysfsHidrawSuffixRE.ReplaceAllString(withoutInput, "")
	return inputSuffixRE.ReplaceAllString(withoutHidraw, "")
}

func stableDeviceID(hidPhys *string, deviceLink string) string {
	source := normalizedDeviceSource(hidPhys, deviceLink)
	h := sha256.Sum256([]byte(source))
	return hex.EncodeToString(h[:10])
}

//nolint:gocyclo // Sysfs parsing and profile matching are inherently branch-heavy but kept in one place.
func candidateFromEntry(entryPath, hidrawPath string, p profile.ProfileSpec) (*model.HidCandidate, bool, error) {
	deviceLink := filepath.Join(entryPath, "device")
	ueventPath := filepath.Join(deviceLink, "uevent")
	descriptorPath := filepath.Join(deviceLink, "report_descriptor")

	uevent, err := parseUevent(ueventPath)
	if err != nil {
		return nil, false, fmt.Errorf("read uevent metadata: %w", err)
	}

	hidID, ok := uevent["HID_ID"]
	if !ok {
		return nil, false, nil
	}

	vendorID, productID, err := parseHidID(hidID)
	if err != nil {
		return nil, false, fmt.Errorf("parse HID_ID %q: %w", hidID, err)
	}

	if vendorID != p.VendorID {
		return nil, false, nil
	}

	transport := p.ClassifyTransport(productID)
	if transport == nil {
		return nil, false, nil
	}

	descriptor, err := discoveryReadFile(descriptorPath)
	if err != nil {
		return nil, false, fmt.Errorf("read HID report descriptor: %w", err)
	}

	inputReportIDs, featureReportIDs, queryReportMatch, wakeReportMatch := descriptorSupportsProfile(descriptor, p)

	if hidrawPath == "" {
		hidrawPath = filepath.Join(discoveryDevRoot, filepath.Base(entryPath))
	}

	hidrawName := filepath.Base(hidrawPath)
	if _, err := discoveryStat(hidrawPath); err != nil {
		return nil, false, fmt.Errorf("stat hidraw path %q: %w", hidrawPath, err)
	}

	hidName := uevent["HID_NAME"]
	if hidName == "" {
		hidName = hidrawName
	}

	var hidPhys *string
	if v, ok := uevent["HID_PHYS"]; ok {
		hidPhys = &v
	}

	interfaceNumber := readInterfaceNumber(deviceLink)
	stableID := stableDeviceID(hidPhys, deviceLink)

	queryMatch := queryReportMatch && endpointMatchesBestEffort(p.QueryEndpoint, interfaceNumber, inputReportIDs, featureReportIDs)
	wakeMatch := wakeReportMatch && endpointMatchesBestEffort(p.WakeEndpoint, interfaceNumber, inputReportIDs, featureReportIDs)

	candidate := &model.HidCandidate{
		HidrawName:       hidrawName,
		Path:             hidrawPath,
		SysfsPath:        entryPath,
		Transport:        *transport,
		InterfaceNumber:  interfaceNumber,
		VendorID:         vendorID,
		ProductID:        productID,
		HidName:          hidName,
		HidPhys:          hidPhys,
		StableDeviceID:   stableID,
		InputReportIDs:   inputReportIDs,
		FeatureReportIDs: featureReportIDs,
	}

	if queryMatch || wakeMatch || p.QueryEndpoint.Matches(*candidate) || p.WakeEndpoint.Matches(*candidate) {
		return candidate, true, nil
	}

	return nil, false, nil
}

type discoveryEntry struct {
	sysfsPath  string
	hidrawPath string
}

// DiscoverHidrawCandidates scans sysfs for hidraw devices matching the given profile and settings.
func DiscoverHidrawCandidates(ctx context.Context, settings config.Settings, p profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
	var entries []discoveryEntry
	var diagnostics []model.DiscoveryDiagnostic

	if settings.ForceHidraw != nil {
		sysfsEntry, hidrawPath, ok := forcedHidrawSysfsEntry(*settings.ForceHidraw)
		if !ok {
			diagnostics = append(diagnostics, model.DiscoveryDiagnostic{
				EntryPath: *settings.ForceHidraw,
				Error:     errors.New("forced hidraw path is invalid or unavailable"),
			})
			return nil, diagnostics
		}

		entries = []discoveryEntry{{
			sysfsPath:  sysfsEntry,
			hidrawPath: hidrawPath,
		}}
	} else {
		matches, err := discoveryGlob(filepath.Join(discoverySysfsRoot, "hidraw*"))
		if err != nil {
			diagnostics = append(diagnostics, model.DiscoveryDiagnostic{
				EntryPath: filepath.Join(discoverySysfsRoot, "hidraw*"),
				Error:     fmt.Errorf("failed to list hidraw entries: %w", err),
			})
			return nil, diagnostics
		}

		sort.Strings(matches)
		entries = make([]discoveryEntry, 0, len(matches))
		for _, match := range matches {
			entries = append(entries, discoveryEntry{
				sysfsPath:  match,
				hidrawPath: filepath.Join(discoveryDevRoot, filepath.Base(match)),
			})
		}
	}

	var candidates []model.HidCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			diagnostics = append(diagnostics, model.DiscoveryDiagnostic{
				EntryPath: discoverySysfsRoot,
				Error:     fmt.Errorf("discovery aborted before processing remaining entries: %w", err),
			})
			break
		}

		candidate, matched, err := candidateFromEntry(entry.sysfsPath, entry.hidrawPath, p)
		if err != nil {
			diagnostics = append(diagnostics, model.DiscoveryDiagnostic{
				EntryPath: entry.sysfsPath,
				Error:     err,
			})
			continue
		}

		if matched {
			candidates = append(candidates, *candidate)
		}
	}

	return candidates, diagnostics
}

func forcedHidrawSysfsEntry(forcedPath string) (string, string, bool) {
	info, err := discoveryStat(forcedPath)
	if err != nil {
		return "", "", false
	}

	if info.Mode()&os.ModeCharDevice == 0 {
		return "", "", false
	}

	hidrawName := filepath.Base(forcedPath)
	if !strings.HasPrefix(hidrawName, "hidraw") {
		return "", "", false
	}

	sysfsEntry := filepath.Join(discoverySysfsRoot, hidrawName)
	if _, err := discoveryStat(sysfsEntry); err != nil {
		return "", "", false
	}

	return sysfsEntry, forcedPath, true
}
