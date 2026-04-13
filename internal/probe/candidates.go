//go:build linux

package probe

import (
	"sort"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

type candidateSortKey struct {
	rank         int
	interfaceNum int
	path         string
}

func candidateSortKeyFn(c model.HidCandidate, ranking map[model.Transport]int) candidateSortKey {
	rank, ok := ranking[c.Transport]
	if !ok {
		rank = len(ranking)
	}

	ifaceNum := 1_000_000
	if c.InterfaceNumber != nil {
		ifaceNum = *c.InterfaceNumber
	}

	return candidateSortKey{rank: rank, interfaceNum: ifaceNum, path: c.Path}
}

// SortCandidates returns a copy of candidates sorted by transport preference and interface number.
func SortCandidates(candidates []model.HidCandidate, settings config.Settings) []model.HidCandidate {
	sorted := make([]model.HidCandidate, len(candidates))
	copy(sorted, candidates)
	ranking := config.TransportRank(settings)

	sortCandidatesBy(sorted, func(c model.HidCandidate) candidateSortKey {
		return candidateSortKeyFn(c, ranking)
	})

	return sorted
}

// BuildProbeTargets groups candidates by stable device ID and creates ProbeTargets with matched query and wake endpoints.
func BuildProbeTargets(candidates []model.HidCandidate, p profile.ProfileSpec) []model.ProbeTarget {
	grouped := make(map[string][]model.HidCandidate)
	for _, c := range candidates {
		grouped[c.StableDeviceID] = append(grouped[c.StableDeviceID], c)
	}

	var targets []model.ProbeTarget
	deviceIDs := make([]string, 0, len(grouped))
	for deviceID := range grouped {
		deviceIDs = append(deviceIDs, deviceID)
	}
	sort.Strings(deviceIDs)

	for _, deviceID := range deviceIDs {
		group := grouped[deviceID]
		var queryCandidate *model.HidCandidate
		for i := range group {
			if p.QueryEndpoint.Matches(group[i]) {
				queryCandidate = &group[i]
				break
			}
		}

		if queryCandidate == nil {
			continue
		}

		wakeCandidate := *queryCandidate
		for i := range group {
			if group[i].Transport == queryCandidate.Transport && p.WakeEndpoint.Matches(group[i]) {
				wakeCandidate = group[i]
				break
			}
		}

		targets = append(targets, model.ProbeTarget{
			DeviceID:       deviceID,
			Transport:      queryCandidate.Transport,
			QueryCandidate: *queryCandidate,
			WakeCandidate:  wakeCandidate,
		})
	}

	return targets
}

func sortCandidatesBy(candidates []model.HidCandidate, keyFn func(model.HidCandidate) candidateSortKey) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return lessCandidateSortKeys(keyFn(candidates[i]), keyFn(candidates[j]))
	})
}

func sortTargetsBy(targets []model.ProbeTarget, ranking map[model.Transport]int) {
	sort.SliceStable(targets, func(i, j int) bool {
		return lessCandidateSortKeys(
			candidateSortKeyFn(targets[i].QueryCandidate, ranking),
			candidateSortKeyFn(targets[j].QueryCandidate, ranking),
		)
	})
}

func lessCandidateSortKeys(left, right candidateSortKey) bool {
	if left.rank != right.rank {
		return left.rank < right.rank
	}

	if left.interfaceNum != right.interfaceNum {
		return left.interfaceNum < right.interfaceNum
	}

	return left.path < right.path
}
