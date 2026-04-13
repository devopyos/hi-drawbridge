//go:build linux

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/probe"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func TestNewBridgeConstructorsDefaultLogger(t *testing.T) {
	settings := testSettings()
	profiles := []profile.ProfileSpec{testProfile("alpha")}

	single := NewHidBatteryBridge(settings, profiles[0], nil, nil)
	if single.logger == nil {
		t.Fatal("expected single bridge logger to default")
	}
	if single.cache == nil {
		t.Fatal("expected single bridge cache to be allocated")
	}

	multi := NewMultiHidBatteryBridge(settings, profiles, nil, nil)
	if multi.logger == nil {
		t.Fatal("expected multi bridge logger to default")
	}
	if len(multi.bridges) != 1 || multi.bridges[0].logger == nil {
		t.Fatal("expected child bridge logger to default")
	}
}

func TestReadingCacheOrdersFallbacksAndPrunes(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	cache := newReadingCache(func() time.Time { return now })
	cache.UpdateMany([]model.BatteryReading{
		testReading("profile", "dev-b", "Bravo", 70),
		testReading("profile", "dev-a", "Alpha", 80),
	})

	fresh := cache.GetFresh(30)
	if got := readingIDs(fresh); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("fresh reading order = %v", got)
	}

	now = now.Add(45 * time.Second)
	fallback, usedStale := cache.GetFallbackReadings(30, 600)
	if !usedStale {
		t.Fatal("expected stale fallback to be used")
	}
	if got := readingIDs(fallback); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("stale reading order = %v", got)
	}

	now = now.Add(10 * time.Minute)
	cache.PruneExpired(600)
	if len(cache.entries) != 0 {
		t.Fatalf("expected cache to be pruned, got %d entries", len(cache.entries))
	}
}

func TestReadingCacheFallbackFreshAndEmpty(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	cache := newReadingCache(func() time.Time { return now })
	readings, usedStale := cache.GetFallbackReadings(30, 600)
	if readings != nil || usedStale {
		t.Fatalf("expected empty fallback, got readings=%v usedStale=%v", readings, usedStale)
	}

	cache.UpdateMany([]model.BatteryReading{testReading("profile", "dev-a", "Alpha", 80)})
	readings, usedStale = cache.GetFallbackReadings(30, 600)
	if usedStale {
		t.Fatal("did not expect stale fallback for fresh data")
	}
	if got := readingIDs(readings); !slices.Equal(got, []string{"dev-a"}) {
		t.Fatalf("unexpected fresh fallback ids %v", got)
	}
}

func TestRunCycleFreshCacheReturnsSortedDevices(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	cache := newReadingCache(func() time.Time { return now })
	cache.UpdateMany([]model.BatteryReading{
		testReading("profile", "dev-b", "Bravo", 70),
		testReading("profile", "dev-a", "Alpha", 80),
	})

	bridge := NewHidBatteryBridge(testSettings(), testProfile("profile"), nil, cache)
	result := bridge.runCycle(context.Background(), true)

	if result.Source != "fresh-cache" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if got := deviceIDs(result.Devices); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("device order = %v", got)
	}
	if got := readingIDs(result.BestReadings); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("best reading order = %v", got)
	}
}

func TestRunCycleNoCandidatesFallsBackToStaleCache(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	cache := newReadingCache(func() time.Time { return now })
	cache.UpdateMany([]model.BatteryReading{testReading("profile", "dev-a", "Alpha", 80)})
	now = now.Add(45 * time.Second)

	bridge := newHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), cache, bridgeDeps{
		discoverCandidates: func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
			return nil, []model.DiscoveryDiagnostic{{
				EntryPath: "/sys/class/hidraw/hidraw0",
				Error:     errors.New("metadata read failed"),
			}}
		},
	})
	result := bridge.runCycle(context.Background(), false)

	if result.Source != "stale-cache-no-candidates" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if !result.UsedStale {
		t.Fatal("expected stale cache flag")
	}
	if !errors.Is(result.Error, model.ErrNoCandidates) {
		t.Fatalf("expected ErrNoCandidates, got %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "metadata read failed") {
		t.Fatalf("expected diagnostic error in %v", result.Error)
	}
	if got := deviceIDs(result.Devices); !slices.Equal(got, []string{"dev-a"}) {
		t.Fatalf("device ids = %v", got)
	}
}

func TestRunCycleSuccessfulProbeSortsAndCachesReadings(t *testing.T) {
	cache := NewReadingCache()
	bridge := newHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), cache, bridgeDeps{
		discoverCandidates: func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
			return []model.HidCandidate{
				testCandidate("dev-b", "/dev/hidraw1"),
				testCandidate("dev-a", "/dev/hidraw0"),
			}, nil
		},
		sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
			return candidates
		},
		buildProbeTargets: func(_ []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
			return []model.ProbeTarget{
				testTarget("dev-b", "/dev/hidraw1"),
				testTarget("dev-a", "/dev/hidraw0"),
			}
		},
		collectBestReadings: func(context.Context, []model.HidCandidate, config.Settings, profile.ProfileSpec, *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
			return []model.BatteryReading{
					testReading("profile", "dev-b", "Bravo", 70),
					testReading("profile", "dev-a", "Alpha", 80),
				}, []model.ProbeResult{{
					CandidatePath: "/dev/hidraw1",
					Success:       true,
				}}
		},
	})
	result := bridge.runCycle(context.Background(), false)

	if result.Source != "probe" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if got := readingIDs(result.BestReadings); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("best reading order = %v", got)
	}
	if got := deviceIDs(result.Devices); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("device order = %v", got)
	}
	if got := readingIDs(cache.GetFresh(30)); !slices.Equal(got, []string{"dev-a", "dev-b"}) {
		t.Fatalf("cached reading order = %v", got)
	}
}

func TestProbeDevicesReturnsChargingState(t *testing.T) {
	bridge := newHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), nil, bridgeDeps{
		discoverCandidates: func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
			return []model.HidCandidate{testCandidate("dev-a", "/dev/hidraw0")}, nil
		},
		sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
			return candidates
		},
		buildProbeTargets: func(_ []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
			return []model.ProbeTarget{testTarget("dev-a", "/dev/hidraw0")}
		},
		collectBestReadings: func(context.Context, []model.HidCandidate, config.Settings, profile.ProfileSpec, *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
			status := 1
			return []model.BatteryReading{{
				DeviceID:   "dev-a",
				Name:       "Alpha",
				Percentage: 81,
				ProfileID:  "profile",
				DeviceType: "mouse",
				IconName:   "input-mouse",
				Status:     &status,
			}}, nil
		},
	})
	devices, err := bridge.ProbeDevices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected one device, got %d", len(devices))
	}
	if !devices[0].IsCharging {
		t.Fatal("expected charging state to be derived from profile bytes")
	}
}

func TestRunCycleCanceledBeforeServingCache(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	cache := newReadingCache(func() time.Time { return now })
	cache.UpdateMany([]model.BatteryReading{testReading("profile", "dev-a", "Alpha", 80)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bridge := NewHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), cache)
	result := bridge.runCycle(ctx, true)

	if result.Source != "canceled" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", result.Error)
	}
	if len(result.Devices) != 0 {
		t.Fatalf("expected no devices, got %d", len(result.Devices))
	}
}

func TestRunCycleCanceledAfterProbeSkipsCacheFallback(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	cache := newReadingCache(func() time.Time { return now })
	cache.UpdateMany([]model.BatteryReading{testReading("profile", "dev-a", "Alpha", 80)})
	now = now.Add(45 * time.Second)

	bridge := newHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), cache, bridgeDeps{
		discoverCandidates: func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
			return []model.HidCandidate{testCandidate("dev-a", "/dev/hidraw0")}, nil
		},
		sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
			return candidates
		},
		buildProbeTargets: func(_ []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
			return []model.ProbeTarget{testTarget("dev-a", "/dev/hidraw0")}
		},
		collectBestReadings: func(context.Context, []model.HidCandidate, config.Settings, profile.ProfileSpec, *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
			cancel()
			return nil, nil
		},
	})
	result := bridge.runCycle(ctx, false)

	if result.Source != "canceled" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", result.Error)
	}
	if result.UsedStale {
		t.Fatal("did not expect stale cache fallback after cancellation")
	}
	if len(result.Devices) != 0 {
		t.Fatalf("expected no devices, got %d", len(result.Devices))
	}
	if len(result.Candidates) != 1 || len(result.Targets) != 1 {
		t.Fatalf("expected preserved discovery context, got %d candidates and %d targets", len(result.Candidates), len(result.Targets))
	}
}

func TestRunCycleCanceledAfterDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bridge := newHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), nil, bridgeDeps{
		discoverCandidates: func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
			cancel()
			return []model.HidCandidate{testCandidate("dev-a", "/dev/hidraw0")}, []model.DiscoveryDiagnostic{{
				EntryPath: "/sys/class/hidraw/hidraw0",
				Error:     errors.New("scan interrupted"),
			}}
		},
		sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
			return candidates
		},
		buildProbeTargets: func(_ []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
			return []model.ProbeTarget{testTarget("dev-a", "/dev/hidraw0")}
		},
	})
	result := bridge.runCycle(ctx, false)
	if result.Source != "canceled" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", result.Error)
	}
	if len(result.DiscoveryDiagnostics) != 1 || len(result.Candidates) != 1 || len(result.Targets) != 1 {
		t.Fatalf("expected canceled result to preserve discovery state: %#v", result)
	}
}

func TestRunCycleProbeFailureFallsBackToCache(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)
	cache := newReadingCache(func() time.Time { return now })
	cache.UpdateMany([]model.BatteryReading{testReading("profile", "dev-a", "Alpha", 80)})
	now = now.Add(45 * time.Second)

	bridge := newHidBatteryBridge(testSettings(), testProfile("profile"), discardLogger(), cache, bridgeDeps{
		discoverCandidates: func(context.Context, config.Settings, profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
			return []model.HidCandidate{testCandidate("dev-a", "/dev/hidraw0")}, []model.DiscoveryDiagnostic{{
				EntryPath: "/sys/class/hidraw/hidraw0",
				Error:     errors.New("diagnostic failure"),
			}}
		},
		sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
			return candidates
		},
		buildProbeTargets: func(_ []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
			return []model.ProbeTarget{testTarget("dev-a", "/dev/hidraw0")}
		},
		collectBestReadings: func(context.Context, []model.HidCandidate, config.Settings, profile.ProfileSpec, *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
			return nil, []model.ProbeResult{{
				CandidatePath: "/dev/hidraw0",
				Success:       false,
				Error:         errors.New("probe failed"),
			}}
		},
	})
	result := bridge.runCycle(context.Background(), false)
	if result.Source != "stale-cache-probe-failed" {
		t.Fatalf("unexpected source %q", result.Source)
	}
	if !result.UsedStale {
		t.Fatal("expected stale cache fallback")
	}
	if !strings.Contains(result.Error.Error(), "probe failed") || !strings.Contains(result.Error.Error(), "diagnostic failure") {
		t.Fatalf("expected joined probe/discovery error, got %v", result.Error)
	}
	if got := deviceIDs(result.Devices); !slices.Equal(got, []string{"dev-a"}) {
		t.Fatalf("unexpected devices %v", got)
	}
}

func TestMultiBridgeGetDevicesCancellationDropsPartialResults(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)

	ctx, cancel := context.WithCancel(context.Background())

	cacheByProfile := map[string]*ReadingCache{
		"alpha": newReadingCache(func() time.Time { return now }),
		"beta":  newReadingCache(func() time.Time { return now }),
	}
	cacheByProfile["alpha"].UpdateMany([]model.BatteryReading{testReading("alpha", "dev-a", "Alpha", 80)})

	multi := newMultiHidBatteryBridge(
		testSettings(),
		[]profile.ProfileSpec{testProfile("alpha"), testProfile("beta")},
		discardLogger(),
		cacheByProfile,
		bridgeDeps{
			discoverCandidates: func(_ context.Context, _ config.Settings, p profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
				if p.ID == "beta" {
					return []model.HidCandidate{testCandidate("dev-b", "/dev/hidraw1")}, nil
				}

				return nil, nil
			},
			sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
				return candidates
			},
			buildProbeTargets: func(_ []model.HidCandidate, p profile.ProfileSpec) []model.ProbeTarget {
				if p.ID == "beta" {
					return []model.ProbeTarget{testTarget("dev-b", "/dev/hidraw1")}
				}

				return nil
			},
			collectBestReadings: func(_ context.Context, _ []model.HidCandidate, _ config.Settings, p profile.ProfileSpec, _ *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
				if p.ID == "beta" {
					cancel()
				}

				return nil, nil
			},
		},
	)

	devices, err := multi.GetDevices(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected no devices, got %d", len(devices))
	}
}

func TestMultiBridgeCanceledEntryPointsReturnEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	multi := NewMultiHidBatteryBridge(
		testSettings(),
		[]profile.ProfileSpec{testProfile("alpha")},
		discardLogger(),
		nil,
	)

	devices, err := multi.GetDevices(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected no devices, got %d", len(devices))
	}

	debug := multi.DebugProbe(ctx)
	if len(debug.Devices) != 0 || len(debug.ProfileResults) != 0 {
		t.Fatalf("expected empty debug result, got %#v", debug)
	}
}

func TestMultiBridgeProbeDevicesStopsBeforeSecondBridgeOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	multi := newMultiHidBatteryBridge(
		testSettings(),
		[]profile.ProfileSpec{testProfile("alpha"), testProfile("beta")},
		discardLogger(),
		nil,
		bridgeDeps{
			discoverCandidates: func(_ context.Context, _ config.Settings, p profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
				return []model.HidCandidate{testCandidate("dev-"+p.ID, "/dev/hidraw0")}, nil
			},
			sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
				return candidates
			},
			buildProbeTargets: func(candidates []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
				targets := make([]model.ProbeTarget, 0, len(candidates))
				for _, candidate := range candidates {
					targets = append(targets, testTarget(candidate.StableDeviceID, candidate.Path))
				}

				return targets
			},
			collectBestReadings: func(_ context.Context, _ []model.HidCandidate, _ config.Settings, p profile.ProfileSpec, _ *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
				if p.ID == "alpha" {
					cancel()
					return []model.BatteryReading{testReading("alpha", "dev-alpha", "Alpha", 80)}, nil
				}

				return []model.BatteryReading{testReading("beta", "dev-beta", "Beta", 20)}, nil
			},
		},
	)

	devices, err := multi.ProbeDevices(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected no devices, got %d", len(devices))
	}
}

func TestMultiBridgeGetDevicesDuplicateIDWarnsAndKeepsFirstProfile(t *testing.T) {
	now := time.Date(2026, time.April, 13, 12, 0, 0, 0, time.UTC)

	logger, buf := newJSONLogger()
	cacheByProfile := map[string]*ReadingCache{
		"alpha": newReadingCache(func() time.Time { return now }),
		"beta":  newReadingCache(func() time.Time { return now }),
	}
	cacheByProfile["alpha"].UpdateMany([]model.BatteryReading{testReading("alpha", "dev-shared", "Alpha Device", 80)})
	cacheByProfile["beta"].UpdateMany([]model.BatteryReading{testReading("beta", "dev-shared", "Beta Device", 20)})

	multi := NewMultiHidBatteryBridge(
		testSettings(),
		[]profile.ProfileSpec{testProfile("alpha"), testProfile("beta")},
		logger,
		cacheByProfile,
	)

	devices, err := multi.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected one merged device, got %d", len(devices))
	}
	if devices[0].Name != "Alpha Device" {
		t.Fatalf("expected first profile to win, got %q", devices[0].Name)
	}

	record := findLogRecord(t, decodeLogRecords(t, buf), "duplicate battery device id across profiles")
	if record["device_id"] != "dev-shared" {
		t.Fatalf("unexpected duplicate log device_id: %#v", record["device_id"])
	}
	if record["kept_profile"] != "alpha" || record["dropped_profile"] != "beta" {
		t.Fatalf("unexpected duplicate log payload: %#v", record)
	}
}

func TestMultiBridgeDebugProbeKeepsFirstDuplicate(t *testing.T) {
	logger, buf := newJSONLogger()
	multi := newMultiHidBatteryBridge(
		testSettings(),
		[]profile.ProfileSpec{testProfile("alpha"), testProfile("beta")},
		logger,
		nil,
		bridgeDeps{
			discoverCandidates: func(_ context.Context, _ config.Settings, p profile.ProfileSpec) ([]model.HidCandidate, []model.DiscoveryDiagnostic) {
				if p.ID == "alpha" {
					return []model.HidCandidate{testCandidate("dev-shared", "/dev/hidraw0")}, nil
				}

				return []model.HidCandidate{testCandidate("dev-shared", "/dev/hidraw1")}, nil
			},
			sortCandidates: func(candidates []model.HidCandidate, _ config.Settings) []model.HidCandidate {
				return candidates
			},
			buildProbeTargets: func(candidates []model.HidCandidate, _ profile.ProfileSpec) []model.ProbeTarget {
				targets := make([]model.ProbeTarget, 0, len(candidates))
				for _, candidate := range candidates {
					targets = append(targets, model.ProbeTarget{
						DeviceID:       candidate.StableDeviceID,
						Transport:      candidate.Transport,
						QueryCandidate: candidate,
						WakeCandidate:  candidate,
					})
				}

				return targets
			},
			collectBestReadings: func(_ context.Context, _ []model.HidCandidate, _ config.Settings, p profile.ProfileSpec, _ *slog.Logger) ([]model.BatteryReading, []model.ProbeResult) {
				name := "Alpha Device"
				percentage := 80
				if p.ID == "beta" {
					name = "Beta Device"
					percentage = 20
				}

				return []model.BatteryReading{testReading(p.ID, "dev-shared", name, percentage)}, nil
			},
		},
	)

	result := multi.DebugProbe(context.Background())
	if len(result.ProfileResults) != 2 {
		t.Fatalf("expected two profile results, got %d", len(result.ProfileResults))
	}
	if len(result.Devices) != 1 {
		t.Fatalf("expected one merged device, got %d", len(result.Devices))
	}
	if result.Devices[0].Name != "Alpha Device" {
		t.Fatalf("expected first profile to win, got %q", result.Devices[0].Name)
	}

	record := findLogRecord(t, decodeLogRecords(t, buf), "duplicate battery device id across profiles")
	if record["kept_profile"] != "alpha" || record["dropped_profile"] != "beta" {
		t.Fatalf("unexpected duplicate log payload: %#v", record)
	}
}

func TestLogSummaryUsesStructuredErrorCode(t *testing.T) {
	logger, buf := newJSONLogger()
	bridge := NewHidBatteryBridge(testSettings(), testProfile("profile"), logger, nil)

	result := model.BridgeRunResult{
		ProfileID: "profile",
		Source:    "probe",
		ProbeResults: []model.ProbeResult{
			{Success: false, Error: fmt.Errorf("wrapped: %w", &probe.FeatureProbeError{Code: probe.FeatureProbeErrorNoValidFrame})},
			{Success: false, Error: &probe.FeatureProbeError{Code: probe.FeatureProbeErrorNoValidFrame}},
			{Success: false, Error: errors.New("temporary")},
		},
	}

	bridge.logSummary(logger, &result)

	record := findLogRecord(t, decodeLogRecords(t, buf), "probe failures detected")
	if record["error_hint"] != "no_valid_frame" {
		t.Fatalf("unexpected error_hint: %#v", record["error_hint"])
	}
}

func TestLogSummaryHandlesFailedProbeWithoutErrorHint(t *testing.T) {
	logger, buf := newJSONLogger()
	bridge := NewHidBatteryBridge(testSettings(), testProfile("profile"), logger, nil)

	result := model.BridgeRunResult{
		ProfileID: "profile",
		Source:    "probe",
		ProbeResults: []model.ProbeResult{
			{Success: false, Error: nil},
		},
	}

	bridge.logSummary(logger, &result)

	record := findLogRecord(t, decodeLogRecords(t, buf), "probe failures detected")
	if _, ok := record["error_hint"]; ok {
		t.Fatalf("did not expect error_hint in %#v", record)
	}
}

func TestHelperComparisonsAndHints(t *testing.T) {
	if compareBatteryReadings(
		model.BatteryReading{DeviceID: "a"},
		model.BatteryReading{DeviceID: "b"},
	) >= 0 {
		t.Fatal("expected device id ordering for readings")
	}
	if compareBatteryReadings(
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0"},
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw1"},
	) >= 0 {
		t.Fatal("expected candidate path ordering for readings")
	}
	if compareBatteryReadings(
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0", Name: "Alpha"},
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0", Name: "Bravo"},
	) >= 0 {
		t.Fatal("expected name ordering for readings")
	}
	if compareBatteryReadings(
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0", Name: "Alpha", Percentage: 10},
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0", Name: "Alpha", Percentage: 20},
	) >= 0 {
		t.Fatal("expected percentage ordering for readings")
	}
	if compareBatteryReadings(
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0", Name: "Alpha", Percentage: 20},
		model.BatteryReading{DeviceID: "a", CandidatePath: "/dev/hidraw0", Name: "Alpha", Percentage: 20},
	) != 0 {
		t.Fatal("expected equal readings to compare as zero")
	}

	if compareBatteryDevices(
		model.BatteryDevice{ID: "a"},
		model.BatteryDevice{ID: "b"},
	) >= 0 {
		t.Fatal("expected device id ordering for devices")
	}
	if compareBatteryDevices(
		model.BatteryDevice{ID: "a", Name: "Alpha"},
		model.BatteryDevice{ID: "a", Name: "Bravo"},
	) >= 0 {
		t.Fatal("expected name ordering for devices")
	}
	if compareBatteryDevices(
		model.BatteryDevice{ID: "a", Name: "Alpha", Percentage: 10},
		model.BatteryDevice{ID: "a", Name: "Alpha", Percentage: 20},
	) >= 0 {
		t.Fatal("expected percentage ordering for devices")
	}
	if compareBatteryDevices(
		model.BatteryDevice{ID: "a", Name: "Alpha", Percentage: 20},
		model.BatteryDevice{ID: "a", Name: "Alpha", Percentage: 20},
	) != 0 {
		t.Fatal("expected equal devices to compare as zero")
	}

	if probeErrorHint(nil) != "" {
		t.Fatal("expected nil error hint to be empty")
	}
}

func testSettings() config.Settings {
	settings := config.DefaultSettings()
	settings.CacheTTLSec = 30
	settings.StaleTTLSec = 600
	return settings
}

func testProfile(id string) profile.ProfileSpec {
	return profile.ProfileSpec{
		ID:                  id,
		ChargingStatusBytes: []int{1},
	}
}

func testReading(profileID, deviceID, name string, percentage int) model.BatteryReading {
	return model.BatteryReading{
		DeviceID:   deviceID,
		Name:       name,
		Percentage: percentage,
		ProfileID:  profileID,
		DeviceType: "mouse",
		IconName:   "input-mouse",
	}
}

func testCandidate(deviceID, path string) model.HidCandidate {
	return model.HidCandidate{
		HidrawName:     path,
		Path:           path,
		Transport:      model.TransportUSBDirect,
		StableDeviceID: deviceID,
	}
}

func testTarget(deviceID, path string) model.ProbeTarget {
	candidate := testCandidate(deviceID, path)
	return model.ProbeTarget{
		DeviceID:       deviceID,
		Transport:      candidate.Transport,
		QueryCandidate: candidate,
		WakeCandidate:  candidate,
	}
}

func readingIDs(readings []model.BatteryReading) []string {
	ids := make([]string, 0, len(readings))
	for _, reading := range readings {
		ids = append(ids, reading.DeviceID)
	}

	return ids
}

func deviceIDs(devices []model.BatteryDevice) []string {
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID)
	}

	return ids
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newJSONLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, buf
}

func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	trimmed := bytes.TrimSpace(buf.Bytes())
	if len(trimmed) == 0 {
		return nil
	}

	lines := bytes.Split(trimmed, []byte("\n"))
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("unmarshal log record: %v", err)
		}
		records = append(records, record)
	}

	return records
}

func findLogRecord(t *testing.T, records []map[string]any, msg string) map[string]any {
	t.Helper()

	for _, record := range records {
		if record["msg"] == msg {
			return record
		}
	}

	t.Fatalf("log message %q not found in %#v", msg, records)
	return nil
}
