//go:build linux

package profile

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

// EndpointSelector specifies criteria for matching a HidCandidate to a query or wake endpoint.
type EndpointSelector struct {
	InterfaceNumbers         []int
	RequiredInputReportIDs   []int
	RequiredFeatureReportIDs []int
}

// Matches returns true if the candidate satisfies all selector constraints.
func (s EndpointSelector) Matches(candidate model.HidCandidate) bool {
	if len(s.InterfaceNumbers) > 0 {
		if candidate.InterfaceNumber == nil {
			return false
		}

		if !slices.Contains(s.InterfaceNumbers, *candidate.InterfaceNumber) {
			return false
		}
	}

	if !IsSubsetInt(s.RequiredInputReportIDs, candidate.InputReportIDs) {
		return false
	}

	return IsSubsetInt(s.RequiredFeatureReportIDs, candidate.FeatureReportIDs)
}

// ProfileSpec defines all parameters needed to query battery status from a specific device model.
type ProfileSpec struct {
	ID                       string
	Name                     string
	SourcePath               string
	VendorID                 string
	TransportProductIDs      map[model.Transport]string
	WakeReport               []byte
	QueryReportID            int
	PrimeQueryCmd            *int
	BatteryQueryCmd          int
	QueryLength              int
	ExpectedSignature        []byte
	BatteryOffset            int
	StatusOffset             int
	ProbePath                model.ProbePath
	FallbackInputReportID    int
	FallbackInputCmd         int
	FallbackInputLength      int
	FallbackBatteryOffset    int
	FallbackBatteryBucketMax int
	DeviceType               string
	IconName                 string
	ChargingStatusBytes      []int
	QueryEndpoint            EndpointSelector
	WakeEndpoint             EndpointSelector
}

// ClassifyTransport returns the matching transport for a product ID, or nil if unknown.
func (p ProfileSpec) ClassifyTransport(productID string) *model.Transport {
	lower := strings.ToLower(productID)

	keys := make([]string, 0, len(p.TransportProductIDs))
	for transport := range p.TransportProductIDs {
		keys = append(keys, string(transport))
	}

	sort.Strings(keys)

	for _, key := range keys {
		transport := model.Transport(key)
		if p.TransportProductIDs[transport] == lower {
			return &transport
		}
	}

	return nil
}

// ProfileRegistry holds the loaded set of device profiles.
type ProfileRegistry struct {
	Profiles    []ProfileSpec
	ProfileByID map[string]ProfileSpec
}

// GetProfile looks up a profile by its case-insensitive ID.
func (r *ProfileRegistry) GetProfile(profileID string) (ProfileSpec, error) {
	normalized := strings.ToLower(strings.TrimSpace(profileID))

	if p, ok := r.ProfileByID[normalized]; ok {
		return p, nil
	}

	known := make([]string, 0, len(r.ProfileByID))
	for id := range r.ProfileByID {
		known = append(known, id)
	}

	sort.Strings(known)

	return ProfileSpec{}, fmt.Errorf("unknown profile %q; known profile ids: %s", profileID, strings.Join(known, ", "))
}

// SelectProfiles returns all profiles when profileID is nil or empty, otherwise the single matching profile.
func (r *ProfileRegistry) SelectProfiles(profileID *string) ([]ProfileSpec, error) {
	if profileID == nil {
		return r.Profiles, nil
	}

	normalized := strings.TrimSpace(*profileID)
	if normalized == "" {
		return r.Profiles, nil
	}

	p, err := r.GetProfile(normalized)
	if err != nil {
		return nil, err
	}

	return []ProfileSpec{p}, nil
}

// BuildProfileRegistry creates a registry from the provided profiles and rejects duplicate IDs.
func BuildProfileRegistry(profiles []ProfileSpec) (*ProfileRegistry, error) {
	ordered := make([]ProfileSpec, len(profiles))
	copy(ordered, profiles)

	byID := make(map[string]ProfileSpec, len(ordered))
	for _, p := range ordered {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if existing, ok := byID[id]; ok {
			return nil, fmt.Errorf(
				"duplicate profile id %q in %s and %s",
				id,
				profileSource(existing),
				profileSource(p),
			)
		}

		byID[id] = p
	}

	return &ProfileRegistry{
		Profiles:    ordered,
		ProfileByID: byID,
	}, nil
}

// IsSubsetInt returns true if every element of subset is present in set.
func IsSubsetInt(subset, set []int) bool {
	for _, s := range subset {
		if !slices.Contains(set, s) {
			return false
		}
	}

	return true
}

func profileSource(p ProfileSpec) string {
	if strings.TrimSpace(p.SourcePath) != "" {
		return p.SourcePath
	}

	return fmt.Sprintf("profile %q", p.ID)
}
