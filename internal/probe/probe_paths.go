//go:build linux

package probe

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func makeProbeResult(
	target model.ProbeTarget,
	wakePath *string,
	wakeError error,
	success bool,
	attempts int,
	frameSource *string,
	probeError error,
	lastFrame []byte,
	reading *model.BatteryReading,
) model.ProbeResult {
	var lastFrameHex *string
	if lastFrame != nil {
		h := hex.EncodeToString(lastFrame)
		lastFrameHex = &h
	}

	return model.ProbeResult{
		CandidatePath:  target.QueryCandidate.Path,
		WakePath:       wakePath,
		HidrawName:     target.QueryCandidate.HidrawName,
		StableDeviceID: target.DeviceID,
		Transport:      target.Transport,
		Success:        success,
		Attempts:       attempts,
		FrameSource:    frameSource,
		Error:          probeError,
		WakeError:      wakeError,
		LastFrameHex:   lastFrameHex,
		Reading:        reading,
	}
}

func sendWake(wakeFd int, wakePath string, p profile.ProfileSpec, logger *slog.Logger) error {
	n, err := probeWrite(wakeFd, p.WakeReport)
	if err != nil {
		if logger != nil {
			logger.Debug("wake report failed", "path", wakePath, "error", formatOSError(err))
		}
		return fmt.Errorf("wake report: %w", err)
	}

	if n != len(p.WakeReport) {
		err := fmt.Errorf("short wake write: wrote %d of %d bytes", n, len(p.WakeReport))
		if logger != nil {
			logger.Debug("wake report failed", "path", wakePath, "error", err.Error())
		}
		return err
	}

	return nil
}

func probeWithFeature(
	ctx context.Context,
	queryFd int,
	target model.ProbeTarget,
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
	wakePath *string,
	wakeError error,
) model.ProbeResult {
	allowInterruptFallback := p.ProbePath == model.ProbePathFeatureOrInterrupt
	retryDelay := time.Duration(settings.RetryDelayMs) * time.Millisecond
	state := featureProbeState{featureReadsEnabled: true}
	attempts := 0
	queryCandidate := target.QueryCandidate

	runPrimeFeatureQuery(ctx, queryFd, p, &state)

	for attempt := 1; attempt <= settings.RetryCount; attempt++ {
		attempts = attempt

		reading, status := runFeatureAttempt(
			ctx,
			queryFd,
			queryCandidate,
			settings,
			p,
			attempt,
			allowInterruptFallback,
			&state,
		)
		if status == featureAttemptStatusSuccess {
			src := string(reading.Source)

			return makeProbeResult(target, wakePath, wakeError, true, attempts, &src, nil, state.lastFrame, reading)
		}

		if status == featureAttemptStatusStop {
			break
		}

		if attempt < settings.RetryCount {
			if !waitWithContext(ctx, retryDelay) {
				state.lastError = ctx.Err()
				break
			}
		}
	}

	if state.lastError == nil {
		state.setErrorCode(FeatureProbeErrorNoValidFrame)
	}

	return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, state.lastError, state.lastFrame, nil)
}

func probeWithOutput(
	ctx context.Context,
	queryFd int,
	target model.ProbeTarget,
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
	wakePath *string,
	wakeError error,
) model.ProbeResult {
	retryDelay := time.Duration(settings.RetryDelayMs) * time.Millisecond
	var lastError error
	var lastFrame []byte
	attempts := 0
	queryCandidate := target.QueryCandidate

	for attempt := 1; attempt <= settings.RetryCount; attempt++ {
		if err := ctx.Err(); err != nil {
			lastError = err
			break
		}

		attempts = attempt

		_, err := probeSendOutputQuery(ctx, queryFd, p.BatteryQueryCmd, p)
		if err != nil {
			lastError = err
			if attempt < settings.RetryCount {
				if !waitWithContext(ctx, retryDelay) {
					lastError = ctx.Err()
					break
				}
			}
			continue
		}

		frame, readErr := probeReadInterruptFrame(ctx, queryFd, p, settings.RetryDelayMs)
		if readErr != nil {
			lastError = readErr
			break
		}

		if frame != nil {
			lastFrame = frame
		}

		reading := decodeInterruptFrame(frame, queryCandidate, p)
		if reading != nil {
			src := string(reading.Source)
			return makeProbeResult(target, wakePath, wakeError, true, attempts, &src, nil, lastFrame, reading)
		}

		if frame != nil {
			lastError = newFeatureProbeErrorCode(FeatureProbeErrorInterruptFrameInvalid)
		} else {
			lastError = newFeatureProbeErrorCode(FeatureProbeErrorNoValidFrame)
		}

		if attempt < settings.RetryCount {
			if !waitWithContext(ctx, retryDelay) {
				lastError = ctx.Err()
				break
			}
		}
	}

	if lastError == nil {
		lastError = newFeatureProbeErrorCode(FeatureProbeErrorNoValidFrame)
	}

	return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, lastError, lastFrame, nil)
}

func probePassive(
	ctx context.Context,
	queryFd int,
	target model.ProbeTarget,
	settings config.Settings,
	p profile.ProfileSpec,
	logger *slog.Logger,
	wakePath *string,
	wakeError error,
) model.ProbeResult {
	totalTimeout := time.Duration(settings.RetryCount*settings.RetryDelayMs) * time.Millisecond
	deadline := time.Now().Add(totalTimeout)
	//nolint:gosec // queryFd comes from a successful unix.Open and is a non-negative OS file descriptor.
	pollFds := []unix.PollFd{{Fd: int32(queryFd), Events: unix.POLLIN | unix.POLLERR | unix.POLLHUP | unix.POLLNVAL}}
	readBuf := make([]byte, 256)
	var lastFrame []byte
	attempts := 1
	queryCandidate := target.QueryCandidate

	for {
		if err := ctx.Err(); err != nil {
			return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, err, lastFrame, nil)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		pollInterval := remaining
		minInterval := time.Duration(max(20, settings.RetryDelayMs)) * time.Millisecond
		if pollInterval > minInterval {
			pollInterval = minInterval
		}

		pollMs := max(1, int(pollInterval.Milliseconds()))

		n, err := probePoll(pollFds, pollMs)
		if err != nil {
			if isRetryablePollErr(err) {
				continue
			}

			return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, fmt.Errorf("poll: %w", err), lastFrame, nil)
		}

		if n == 0 {
			continue
		}

		if pollFds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, fmt.Errorf("hidraw poll: error, hangup, or invalid fd on fd %d", queryFd), lastFrame, nil)
		}

		for range n {
			nRead, readErr := probeRead(queryFd, readBuf)
			if readErr != nil {
				if isRetryableReadErr(readErr) {
					continue
				}

				return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, fmt.Errorf("read: %w", readErr), lastFrame, nil)
			}

			if nRead == 0 {
				continue
			}

			frame := readBuf[:nRead]
			lastFrame = append(lastFrame[:0], frame...)
			reading := decodeInterruptFrame(frame, queryCandidate, p)
			if reading != nil {
				src := string(reading.Source)
				return makeProbeResult(target, wakePath, wakeError, true, attempts, &src, nil, lastFrame, reading)
			}
		}
	}

	return makeProbeResult(target, wakePath, wakeError, false, attempts, nil, newFeatureProbeErrorCode(FeatureProbeErrorNoValidFrame), lastFrame, nil)
}
