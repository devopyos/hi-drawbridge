//go:build linux

package bridge

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/discovery"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/probe"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

type (
	discoverCandidatesFunc  func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic)
	sortCandidatesFunc      func([]model.HidCandidate, config.Settings) []model.HidCandidate
	buildProbeTargetsFunc   func([]model.HidCandidate, profile.ProfileSpec) []model.ProbeTarget
	collectBestReadingsFunc func(context.Context, []model.HidCandidate, config.Settings, profile.ProfileSpec, *slog.Logger) ([]model.BatteryReading, []model.ProbeResult)
)

type bridgeDeps struct {
	now                 func() time.Time
	discoverCandidates  discoverCandidatesFunc
	sortCandidates      sortCandidatesFunc
	buildProbeTargets   buildProbeTargetsFunc
	collectBestReadings collectBestReadingsFunc
}

func defaultBridgeDeps() bridgeDeps {
	return bridgeDeps{
		now:                 time.Now,
		discoverCandidates:  discovery.DiscoverHidrawCandidates,
		sortCandidates:      probe.SortCandidates,
		buildProbeTargets:   probe.BuildProbeTargets,
		collectBestReadings: probe.CollectBestReadings,
	}
}

func resolveBridgeDeps(deps bridgeDeps) bridgeDeps {
	defaults := defaultBridgeDeps()
	if deps.now == nil {
		deps.now = defaults.now
	}
	if deps.discoverCandidates == nil {
		deps.discoverCandidates = defaults.discoverCandidates
	}
	if deps.sortCandidates == nil {
		deps.sortCandidates = defaults.sortCandidates
	}
	if deps.buildProbeTargets == nil {
		deps.buildProbeTargets = defaults.buildProbeTargets
	}
	if deps.collectBestReadings == nil {
		deps.collectBestReadings = defaults.collectBestReadings
	}

	return deps
}

type cacheEntry struct {
	reading   model.BatteryReading
	updatedAt time.Time
}

// ReadingCache stores battery readings keyed by device ID with TTL-based freshness tracking.
type ReadingCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	now     func() time.Time
}

// NewReadingCache creates an empty ReadingCache.
func NewReadingCache() *ReadingCache {
	return newReadingCache(time.Now)
}

func newReadingCache(now func() time.Time) *ReadingCache {
	if now == nil {
		now = time.Now
	}

	return &ReadingCache{
		entries: make(map[string]cacheEntry),
		now:     now,
	}
}

func (c *ReadingCache) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// UpdateMany replaces or inserts the given readings in the cache, stamping them with the current time.
func (c *ReadingCache) UpdateMany(readings []model.BatteryReading) {
	now := c.nowTime()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range readings {
		c.entries[r.DeviceID] = cacheEntry{reading: r, updatedAt: now}
	}
}

// PruneExpired removes entries older than maxAgeSec seconds.
func (c *ReadingCache) PruneExpired(maxAgeSec int) {
	cutoff := c.nowTime().Add(-time.Duration(maxAgeSec) * time.Second)
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, entry := range c.entries {
		if entry.updatedAt.Before(cutoff) {
			delete(c.entries, id)
		}
	}
}

// GetFresh returns readings updated within the last ttlSec seconds.
func (c *ReadingCache) GetFresh(ttlSec int) []model.BatteryReading {
	cutoff := c.nowTime().Add(-time.Duration(ttlSec) * time.Second)
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []model.BatteryReading
	for _, entry := range c.entries {
		if !entry.updatedAt.Before(cutoff) {
			result = append(result, entry.reading)
		}
	}

	sortBatteryReadings(result)
	return result
}

// GetStale returns readings that are older than cacheTTLSec but newer than staleTTLSec.
func (c *ReadingCache) GetStale(cacheTTLSec, staleTTLSec int) []model.BatteryReading {
	freshCutoff := c.nowTime().Add(-time.Duration(cacheTTLSec) * time.Second)
	staleCutoff := c.nowTime().Add(-time.Duration(staleTTLSec) * time.Second)
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []model.BatteryReading
	for _, entry := range c.entries {
		if entry.updatedAt.Before(freshCutoff) && !entry.updatedAt.Before(staleCutoff) {
			result = append(result, entry.reading)
		}
	}

	sortBatteryReadings(result)
	return result
}

// GetFallbackReadings returns fresh readings if available, otherwise stale readings.
// The second return value indicates whether stale data was used.
func (c *ReadingCache) GetFallbackReadings(cacheTTLSec, staleTTLSec int) ([]model.BatteryReading, bool) {
	freshReadings := c.GetFresh(cacheTTLSec)
	if len(freshReadings) > 0 {
		return freshReadings, false
	}

	staleReadings := c.GetStale(cacheTTLSec, staleTTLSec)
	if len(staleReadings) > 0 {
		return staleReadings, true
	}

	return nil, false
}

// HidBatteryBridge probes a single device profile for battery readings with caching.
type HidBatteryBridge struct {
	settings config.Settings
	profile  profile.ProfileSpec
	logger   *slog.Logger
	cache    *ReadingCache
	deps     bridgeDeps
}

// NewHidBatteryBridge creates a bridge for the given profile and settings.
// If cache is nil, a new ReadingCache is allocated.
func NewHidBatteryBridge(
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
	cache *ReadingCache,
) *HidBatteryBridge {
	return newHidBatteryBridge(settings, p, logger, cache, bridgeDeps{})
}

func newHidBatteryBridge(
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
	cache *ReadingCache,
	deps bridgeDeps,
) *HidBatteryBridge {
	deps = resolveBridgeDeps(deps)
	if cache == nil {
		cache = newReadingCache(deps.now)
	}
	logger = resolveLogger(logger)

	return &HidBatteryBridge{
		settings: settings,
		profile:  p,
		logger:   logger,
		cache:    cache,
		deps:     deps,
	}
}

// GetDevices returns battery devices, using fresh cached data when available.
func (b *HidBatteryBridge) GetDevices(ctx context.Context) ([]model.BatteryDevice, error) {
	result := b.runCycle(ctx, true)
	return result.Devices, result.Error
}

// ProbeDevices forces a new probe cycle and returns the discovered battery devices.
func (b *HidBatteryBridge) ProbeDevices(ctx context.Context) ([]model.BatteryDevice, error) {
	result := b.runCycle(ctx, false)
	return result.Devices, result.Error
}

// DebugProbe runs a full probe cycle and returns the detailed BridgeRunResult.
func (b *HidBatteryBridge) DebugProbe(ctx context.Context) model.BridgeRunResult {
	return b.runCycle(ctx, false)
}

type bridgeCycleState struct {
	candidates           []model.HidCandidate
	targets              []model.ProbeTarget
	discoveryDiagnostics []model.DiscoveryDiagnostic
	probeResults         []model.ProbeResult
}

func (b *HidBatteryBridge) runCycle(ctx context.Context, useFreshCache bool) model.BridgeRunResult {
	logger := resolveLogger(b.logger)
	if result, ok := b.canceledResultFromContext(ctx, bridgeCycleState{}); ok {
		return b.finishCycle(logger, result)
	}

	b.cache.PruneExpired(b.settings.StaleTTLSec)

	if result, ok := b.tryFreshCache(ctx, useFreshCache); ok {
		return b.finishCycle(logger, result)
	}

	state := b.discoverCycle(ctx)
	if result, ok := b.canceledResultFromContext(ctx, state); ok {
		return b.finishCycle(logger, result)
	}

	if len(state.candidates) == 0 {
		return b.finishCycle(logger, b.noCandidatesResult(state))
	}

	bestReadings, probeResults := b.deps.collectBestReadings(
		ctx, state.candidates, b.settings, b.profile, logger,
	)
	state.probeResults = probeResults
	if result, ok := b.canceledResultFromContext(ctx, state); ok {
		return b.finishCycle(logger, result)
	}

	bestReadings = cloneReadings(bestReadings)
	sortBatteryReadings(bestReadings)

	if len(bestReadings) > 0 {
		return b.finishCycle(logger, b.probeSuccessResult(state, bestReadings))
	}

	return b.finishCycle(logger, b.probeFailureResult(state))
}

func (b *HidBatteryBridge) tryFreshCache(ctx context.Context, useFreshCache bool) (model.BridgeRunResult, bool) {
	if !useFreshCache {
		return model.BridgeRunResult{}, false
	}

	freshReadings := b.cache.GetFresh(b.settings.CacheTTLSec)
	if err := ctx.Err(); err != nil {
		return newCanceledResult(b.profile.ID, err), true
	}
	if len(freshReadings) == 0 {
		return model.BridgeRunResult{}, false
	}

	return model.BridgeRunResult{
		ProfileID:    b.profile.ID,
		Source:       "fresh-cache",
		Devices:      b.toDevices(freshReadings),
		BestReadings: freshReadings,
		UsedStale:    false,
	}, true
}

func (b *HidBatteryBridge) discoverCycle(ctx context.Context) bridgeCycleState {
	rawCandidates, discoveryDiagnostics := b.deps.discoverCandidates(ctx, b.settings, b.profile)
	candidates := b.deps.sortCandidates(rawCandidates, b.settings)
	targets := b.deps.buildProbeTargets(candidates, b.profile)

	return bridgeCycleState{
		candidates:           candidates,
		targets:              targets,
		discoveryDiagnostics: discoveryDiagnostics,
	}
}

func (b *HidBatteryBridge) canceledResultFromContext(ctx context.Context, state bridgeCycleState) (model.BridgeRunResult, bool) {
	if err := ctx.Err(); err != nil {
		return b.canceledResult(err, state), true
	}

	return model.BridgeRunResult{}, false
}

func (b *HidBatteryBridge) canceledResult(err error, state bridgeCycleState) model.BridgeRunResult {
	result := newCanceledResult(b.profile.ID, err)
	result.Candidates = state.candidates
	result.Targets = state.targets
	result.ProbeResults = state.probeResults
	result.DiscoveryDiagnostics = state.discoveryDiagnostics
	return result
}

func (b *HidBatteryBridge) noCandidatesResult(state bridgeCycleState) model.BridgeRunResult {
	fallbackReadings, usedStale := b.cache.GetFallbackReadings(b.settings.CacheTTLSec, b.settings.StaleTTLSec)

	return model.BridgeRunResult{
		ProfileID:            b.profile.ID,
		Source:               noCandidateSource(fallbackReadings, usedStale),
		Devices:              b.toDevices(fallbackReadings),
		Targets:              state.targets,
		BestReadings:         fallbackReadings,
		DiscoveryDiagnostics: state.discoveryDiagnostics,
		UsedStale:            usedStale,
		Error:                errors.Join(model.ErrNoCandidates, joinDiscoveryErrors(state.discoveryDiagnostics)),
	}
}

func (b *HidBatteryBridge) probeSuccessResult(state bridgeCycleState, bestReadings []model.BatteryReading) model.BridgeRunResult {
	b.cache.UpdateMany(bestReadings)

	return model.BridgeRunResult{
		ProfileID:            b.profile.ID,
		Source:               "probe",
		Devices:              b.toDevices(bestReadings),
		Candidates:           state.candidates,
		Targets:              state.targets,
		ProbeResults:         state.probeResults,
		BestReadings:         bestReadings,
		DiscoveryDiagnostics: state.discoveryDiagnostics,
		UsedStale:            false,
	}
}

func (b *HidBatteryBridge) probeFailureResult(state bridgeCycleState) model.BridgeRunResult {
	fallbackReadings, usedStale := b.cache.GetFallbackReadings(b.settings.CacheTTLSec, b.settings.StaleTTLSec)

	return model.BridgeRunResult{
		ProfileID:            b.profile.ID,
		Source:               probeFailureSource(fallbackReadings, usedStale),
		Devices:              b.toDevices(fallbackReadings),
		Candidates:           state.candidates,
		Targets:              state.targets,
		ProbeResults:         state.probeResults,
		BestReadings:         fallbackReadings,
		DiscoveryDiagnostics: state.discoveryDiagnostics,
		UsedStale:            usedStale,
		Error:                errors.Join(joinDiscoveryErrors(state.discoveryDiagnostics), joinProbeErrors(state.probeResults)),
	}
}

func (b *HidBatteryBridge) finishCycle(logger *slog.Logger, result model.BridgeRunResult) model.BridgeRunResult {
	b.logSummary(logger, &result)
	return result
}

func noCandidateSource(fallbackReadings []model.BatteryReading, usedStale bool) string {
	if len(fallbackReadings) == 0 {
		return "no-candidates"
	}
	if usedStale {
		return "stale-cache-no-candidates"
	}

	return "cache-no-candidates"
}

func probeFailureSource(fallbackReadings []model.BatteryReading, usedStale bool) string {
	if len(fallbackReadings) == 0 {
		return "empty"
	}
	if usedStale {
		return "stale-cache-probe-failed"
	}

	return "cache-probe-failed"
}

func joinDiscoveryErrors(diagnostics []model.DiscoveryDiagnostic) error {
	var joined error
	for _, d := range diagnostics {
		if d.Error != nil {
			joined = errors.Join(joined, d.Error)
		}
	}

	return joined
}

func joinProbeErrors(results []model.ProbeResult) error {
	var joined error
	for _, result := range results {
		if result.Error != nil {
			joined = errors.Join(joined, result.Error)
		}
	}

	return joined
}

func (b *HidBatteryBridge) toDevices(readings []model.BatteryReading) []model.BatteryDevice {
	devices := make([]model.BatteryDevice, 0, len(readings))
	for _, r := range readings {
		isCharging := r.Status != nil && containsInt(b.profile.ChargingStatusBytes, *r.Status)
		devices = append(devices, model.BatteryDevice{
			ID:         r.DeviceID,
			Name:       r.Name,
			DeviceType: r.DeviceType,
			IconName:   r.IconName,
			Percentage: r.Percentage,
			IsCharging: isCharging,
		})
	}
	sortBatteryDevices(devices)
	return devices
}

func (b *HidBatteryBridge) logSummary(logger *slog.Logger, result *model.BridgeRunResult) {
	successfulProbes := 0
	failedProbes := 0
	errorCounts := make(map[string]int)
	firstSeen := make(map[string]int)
	lastErrorHint := ""

	for i, pr := range result.ProbeResults {
		if pr.Success {
			successfulProbes++
			continue
		}

		failedProbes++
		if pr.Error == nil {
			continue
		}

		hint := probeErrorHint(pr.Error)
		errorCounts[hint]++
		if _, ok := firstSeen[hint]; !ok {
			firstSeen[hint] = i
		}
		lastErrorHint = hint
	}

	frequentErrorHint := ""
	highestCount := 0
	earliestIndex := len(result.ProbeResults)
	for errText, count := range errorCounts {
		index := firstSeen[errText]
		if count > highestCount || (count == highestCount && index < earliestIndex) {
			highestCount = count
			earliestIndex = index
			frequentErrorHint = errText
		}
	}

	logger.Debug("bridge cycle",
		"profile", b.profile.ID,
		"source", result.Source,
		"candidates", len(result.Candidates),
		"valid_probes", successfulProbes,
		"failed_probes", failedProbes,
		"stale_used", result.UsedStale,
		"devices", len(result.Devices),
	)

	if failedProbes > 0 {
		hint := frequentErrorHint
		if hint == "" {
			hint = lastErrorHint
		}

		fields := []any{
			"profile", b.profile.ID,
			"source", result.Source,
			"failed_probes", failedProbes,
			"total_probes", len(result.ProbeResults),
		}
		if hint != "" {
			fields = append(fields, "error_hint", hint)
		}

		logger.Info("probe failures detected", fields...)
	}
}

// MultiHidBatteryBridge probes multiple device profiles and merges the results.
type MultiHidBatteryBridge struct {
	logger  *slog.Logger
	bridges []*HidBatteryBridge
}

// NewMultiHidBatteryBridge creates a multi-profile bridge with an optional per-profile cache map.
func NewMultiHidBatteryBridge(
	settings config.Settings,
	profiles []profile.ProfileSpec,
	logger *slog.Logger,
	cacheByProfile map[string]*ReadingCache,
) *MultiHidBatteryBridge {
	return newMultiHidBatteryBridge(settings, profiles, logger, cacheByProfile, bridgeDeps{})
}

func newMultiHidBatteryBridge(
	settings config.Settings,
	profiles []profile.ProfileSpec,
	logger *slog.Logger,
	cacheByProfile map[string]*ReadingCache,
	deps bridgeDeps,
) *MultiHidBatteryBridge {
	deps = resolveBridgeDeps(deps)
	logger = resolveLogger(logger)
	bridges := make([]*HidBatteryBridge, 0, len(profiles))
	for _, p := range profiles {
		var cache *ReadingCache
		if cacheByProfile != nil {
			cache = cacheByProfile[p.ID]
		}
		bridges = append(bridges, newHidBatteryBridge(settings, p, logger, cache, deps))
	}

	return &MultiHidBatteryBridge{
		logger:  logger,
		bridges: bridges,
	}
}

// GetDevices returns merged battery devices across all profiles, using fresh cached data when available.
func (m *MultiHidBatteryBridge) GetDevices(ctx context.Context) ([]model.BatteryDevice, error) {
	return m.collectDevices(ctx, true)
}

// ProbeDevices forces a new probe cycle across all profiles and returns the merged devices.
func (m *MultiHidBatteryBridge) ProbeDevices(ctx context.Context) ([]model.BatteryDevice, error) {
	return m.collectDevices(ctx, false)
}

// DebugProbe runs a full probe cycle across all profiles and returns detailed per-profile results.
func (m *MultiHidBatteryBridge) DebugProbe(ctx context.Context) model.MultiBridgeRunResult {
	if err := ctx.Err(); err != nil {
		return model.MultiBridgeRunResult{}
	}

	profileResults := make([]model.BridgeRunResult, 0, len(m.bridges))
	merged := newMergedDeviceSet(resolveLogger(m.logger))
	for _, bridge := range m.bridges {
		if ctx.Err() != nil {
			break
		}

		result := bridge.DebugProbe(ctx)
		profileResults = append(profileResults, result)
		merged.Add(bridge.profile.ID, result.Devices)
		if ctx.Err() != nil {
			break
		}
	}

	return model.MultiBridgeRunResult{
		Devices:        merged.Devices(),
		ProfileResults: profileResults,
	}
}

func (m *MultiHidBatteryBridge) collectDevices(ctx context.Context, useFreshCache bool) ([]model.BatteryDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	merged := newMergedDeviceSet(resolveLogger(m.logger))
	var allErrors []error
	for _, bridge := range m.bridges {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(append(allErrors, err)...)
		}

		var devices []model.BatteryDevice
		var err error
		if useFreshCache {
			devices, err = bridge.GetDevices(ctx)
		} else {
			devices, err = bridge.ProbeDevices(ctx)
		}
		if err != nil {
			allErrors = append(allErrors, err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, errors.Join(allErrors...)
			}
		}
		merged.Add(bridge.profile.ID, devices)
	}
	return merged.Devices(), errors.Join(allErrors...)
}

type mergedDeviceSet struct {
	logger      *slog.Logger
	byID        map[string]model.BatteryDevice
	profileByID map[string]string
}

func newMergedDeviceSet(logger *slog.Logger) *mergedDeviceSet {
	return &mergedDeviceSet{
		logger:      resolveLogger(logger),
		byID:        make(map[string]model.BatteryDevice),
		profileByID: make(map[string]string),
	}
}

// Add merges devices into the set. When multiple profiles report the same device ID,
// the earliest selected profile wins and later duplicates are ignored with a warning.
func (m *mergedDeviceSet) Add(profileID string, devices []model.BatteryDevice) {
	for _, d := range devices {
		if keptProfileID, exists := m.profileByID[d.ID]; exists {
			if keptProfileID != profileID {
				m.logger.Warn(
					"duplicate battery device id across profiles",
					"device_id", d.ID,
					"kept_profile", keptProfileID,
					"dropped_profile", profileID,
				)
			}
			continue
		}

		m.profileByID[d.ID] = profileID
		m.byID[d.ID] = d
	}
}

func (m *mergedDeviceSet) Devices() []model.BatteryDevice {
	result := make([]model.BatteryDevice, 0, len(m.byID))
	for _, id := range sortedKeys(m.byID) {
		result = append(result, m.byID[id])
	}
	return result
}

func sortedKeys(m map[string]model.BatteryDevice) []string {
	return slices.Sorted(maps.Keys(m))
}

func containsInt(slice []int, val int) bool {
	return slices.Contains(slice, val)
}

func resolveLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}

	return logger
}

func newCanceledResult(profileID string, err error) model.BridgeRunResult {
	return model.BridgeRunResult{
		ProfileID: profileID,
		Source:    "canceled",
		Error:     err,
	}
}

func sortBatteryReadings(readings []model.BatteryReading) {
	slices.SortFunc(readings, compareBatteryReadings)
}

func sortBatteryDevices(devices []model.BatteryDevice) {
	slices.SortFunc(devices, compareBatteryDevices)
}

func compareBatteryReadings(a, b model.BatteryReading) int {
	if a.DeviceID < b.DeviceID {
		return -1
	}
	if a.DeviceID > b.DeviceID {
		return 1
	}
	if a.CandidatePath < b.CandidatePath {
		return -1
	}
	if a.CandidatePath > b.CandidatePath {
		return 1
	}
	if a.Name < b.Name {
		return -1
	}
	if a.Name > b.Name {
		return 1
	}
	if a.Percentage < b.Percentage {
		return -1
	}
	if a.Percentage > b.Percentage {
		return 1
	}

	return 0
}

func compareBatteryDevices(a, b model.BatteryDevice) int {
	if a.ID < b.ID {
		return -1
	}
	if a.ID > b.ID {
		return 1
	}
	if a.Name < b.Name {
		return -1
	}
	if a.Name > b.Name {
		return 1
	}
	if a.Percentage < b.Percentage {
		return -1
	}
	if a.Percentage > b.Percentage {
		return 1
	}

	return 0
}

func cloneReadings(readings []model.BatteryReading) []model.BatteryReading {
	out := make([]model.BatteryReading, len(readings))
	copy(out, readings)
	return out
}

func probeErrorHint(err error) string {
	if err == nil {
		return ""
	}

	var coder model.ErrorCoder
	if errors.As(err, &coder) {
		if code := coder.ErrorCode(); code != "" {
			return code
		}
	}

	return err.Error()
}
