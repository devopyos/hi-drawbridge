//go:build linux

package probe

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

// ProbeTarget opens hidraw devices and probes a single target for battery data using the profile's configured path.
//
//nolint:gocyclo // inherent complexity: fd lifecycle, wake, probe path selection, fd-close tracking
func ProbeTarget(
	ctx context.Context,
	target model.ProbeTarget,
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
) model.ProbeResult {
	if err := ctx.Err(); err != nil {
		return makeProbeResult(target, nil, nil, false, 0, nil, err, nil, nil)
	}

	queryCandidate := target.QueryCandidate
	wakeCandidate := target.WakeCandidate
	useWakeFD := p.ProbePath != model.ProbePathPassive

	queryFd, err := probeOpen(queryCandidate.Path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return makeProbeResult(target, nil, nil, false, 0, nil, fmt.Errorf("open query fd: %w", err), nil, nil)
	}

	wakeFd := queryFd
	if useWakeFD && wakeCandidate.Path != queryCandidate.Path {
		wakeFd, err = probeOpen(wakeCandidate.Path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err != nil {
			closeFD(queryFd, queryCandidate.Path, logger)
			return makeProbeResult(target, nil, nil, false, 0, nil, fmt.Errorf("open wake fd: %w", err), nil, nil)
		}
	}

	closed := &fdCloseTracker{queryFd: queryFd, wakeFd: wakeFd}

	defer func() {
		if wakeFd != queryFd && !closed.wakeClosed {
			closeFD(wakeFd, wakeCandidate.Path, logger)
		}
		if !closed.queryClosed {
			closeFD(queryFd, queryCandidate.Path, logger)
		}
	}()

	retryDelay := time.Duration(settings.RetryDelayMs) * time.Millisecond
	var wakePath *string
	if useWakeFD && wakeCandidate.Path != queryCandidate.Path {
		wp := wakeCandidate.Path
		wakePath = &wp
	}

	var wakeError error
	if p.ProbePath != model.ProbePathPassive {
		wakeError = sendWake(wakeFd, wakeCandidate.Path, p, logger)
		closed.markIfClosed(wakeError, wakeFd)
		if !waitWithContext(ctx, retryDelay) {
			return makeProbeResult(target, wakePath, wakeError, false, 0, nil, ctx.Err(), nil, nil)
		}
	}

	var result model.ProbeResult

	switch p.ProbePath {
	case model.ProbePathFeatureOnly, model.ProbePathFeatureOrInterrupt:
		result = probeWithFeature(ctx, queryFd, target, settings, p, logger, wakePath, wakeError)
	case model.ProbePathInterruptOnly:
		result = probeWithOutput(ctx, queryFd, target, settings, p, logger, wakePath, wakeError)
	case model.ProbePathPassive:
		result = probePassive(ctx, queryFd, target, settings, p, logger, wakePath, wakeError)
	default:
		result = makeProbeResult(target, wakePath, wakeError, false, 0, nil, fmt.Errorf("unsupported probe path: %s", p.ProbePath), nil, nil)
	}

	closed.markIfClosed(result.Error, queryFd)

	return result
}

// CollectBestReadings discovers candidates, builds targets, probes each one, and returns the best reading per device.
func CollectBestReadings(
	ctx context.Context,
	candidates []model.HidCandidate,
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
) ([]model.BatteryReading, []model.ProbeResult) {
	targets := BuildProbeTargets(candidates, p)
	ranking := config.TransportRank(settings)

	sortedTargets := make([]model.ProbeTarget, len(targets))
	copy(sortedTargets, targets)
	sortTargetsBy(sortedTargets, ranking)

	var bestReadings []model.BatteryReading
	var probeResults []model.ProbeResult
	seenDeviceIDs := make(map[string]bool)
	winnerRankByDeviceID := make(map[string]int)

	for _, target := range sortedTargets {
		if ctx.Err() != nil {
			break
		}

		targetRank, ok := ranking[target.Transport]
		if !ok {
			targetRank = len(ranking)
		}

		if winnerRank, exists := winnerRankByDeviceID[target.DeviceID]; exists && targetRank > winnerRank {
			continue
		}

		result := ProbeTarget(ctx, target, settings, p, logger)
		probeResults = append(probeResults, result)

		if !result.Success || result.Reading == nil {
			continue
		}

		winnerRankByDeviceID[target.DeviceID] = targetRank

		if seenDeviceIDs[result.Reading.DeviceID] {
			continue
		}

		seenDeviceIDs[result.Reading.DeviceID] = true
		bestReadings = append(bestReadings, *result.Reading)
	}

	return bestReadings, probeResults
}
