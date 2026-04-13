//go:build linux

package probe

import (
	"context"
	"time"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/hidio"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

var probeSendQuery = hidio.SendQuery

var probeRecvFeatureReport = hidio.RecvFeatureReport

var probeReadInterruptFrame = hidio.ReadInterruptFrame

var probeSendOutputQuery = hidio.SendOutputQuery

type featureProbeState struct {
	featureReadsEnabled bool
	lastError           error
	lastFrame           []byte
}

func waitWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *featureProbeState) setErrorCode(code FeatureProbeErrorCode) {
	s.lastError = newFeatureProbeErrorCode(code)
}

func (s *featureProbeState) setFeatureReadError(err error) {
	if isEPIPE(err) {
		s.featureReadsEnabled = false
		s.setErrorCode(FeatureProbeErrorReadEPIPE)

		return
	}

	s.lastError = featureProbeErrorf(formatOSError(err))
}

func runPrimeFeatureQuery(ctx context.Context, queryFd int, p profile.ProfileSpec, state *featureProbeState) {
	if p.PrimeQueryCmd == nil {
		return
	}

	if ctx.Err() != nil {
		return
	}

	_, err := probeSendQuery(ctx, queryFd, *p.PrimeQueryCmd, p)
	if err != nil {
		state.lastError = featureProbeErrorf(err.Error())

		return
	}

	//nolint:gosec // query_report_id is validated as a byte during profile config loading.
	_, err = probeRecvFeatureReport(ctx, queryFd, byte(p.QueryReportID), p.QueryLength)
	if err != nil {
		state.setFeatureReadError(err)
	}
}

func maxFeatureReads(p profile.ProfileSpec) int {
	if p.PrimeQueryCmd != nil {
		return 3
	}

	return 2
}

func readFeatureProbeAttempt(
	ctx context.Context,
	queryFd int,
	queryCandidate model.HidCandidate,
	p profile.ProfileSpec,
	state *featureProbeState,
) ([]byte, *model.BatteryReading) {
	var featureFrame []byte

	for range maxFeatureReads(p) {
		if ctx.Err() != nil {
			break
		}

		//nolint:gosec // query_report_id is validated as a byte during profile config loading.
		frame, err := probeRecvFeatureReport(ctx, queryFd, byte(p.QueryReportID), p.QueryLength)
		if err != nil {
			state.setFeatureReadError(err)

			break
		}

		featureFrame = frame
		state.lastFrame = frame

		reading := decodeFeatureFrame(frame, queryCandidate, p)
		if reading != nil {
			return featureFrame, reading
		}
	}

	return featureFrame, nil
}

func shouldRetryFeatureRead(
	attempt int,
	retryCount int,
	allowInterruptFallback bool,
	featureFrame []byte,
	state *featureProbeState,
) bool {
	if !allowInterruptFallback || !state.featureReadsEnabled || attempt >= retryCount {
		return false
	}

	if featureFrame != nil {
		state.setErrorCode(FeatureProbeErrorFrameInvalid)

		return true
	}

	if state.lastError == nil {
		state.setErrorCode(FeatureProbeErrorNoValidFrame)
	}

	return true
}

func updateFeatureAttemptError(
	allowInterruptFallback bool,
	featureFrame []byte,
	state *featureProbeState,
) {
	if featureFrame != nil {
		state.setErrorCode(FeatureProbeErrorFrameInvalid)

		return
	}

	if !allowInterruptFallback && state.lastError != nil {
		return
	}

	state.setErrorCode(FeatureProbeErrorNoValidFrame)
}

type featureAttemptStatus uint8

const (
	featureAttemptStatusSuccess featureAttemptStatus = iota
	featureAttemptStatusRetry
	featureAttemptStatusStop
)

func runFeatureAttempt(
	ctx context.Context,
	queryFd int,
	queryCandidate model.HidCandidate,
	settings config.Settings,
	p profile.ProfileSpec,
	attempt int,
	allowInterruptFallback bool,
	state *featureProbeState,
) (*model.BatteryReading, featureAttemptStatus) {
	if err := ctx.Err(); err != nil {
		state.lastError = err

		return nil, featureAttemptStatusStop
	}

	_, err := probeSendQuery(ctx, queryFd, p.BatteryQueryCmd, p)
	if err != nil {
		state.lastError = err

		return nil, featureAttemptStatusRetry
	}

	var featureFrame []byte
	if state.featureReadsEnabled {
		var featureReading *model.BatteryReading
		featureFrame, featureReading = readFeatureProbeAttempt(ctx, queryFd, queryCandidate, p, state)
		if featureReading != nil {
			return featureReading, featureAttemptStatusSuccess
		}
	}

	if shouldRetryFeatureRead(attempt, settings.RetryCount, allowInterruptFallback, featureFrame, state) {
		return nil, featureAttemptStatusRetry
	}

	if allowInterruptFallback {
		interruptFrame, readErr := probeReadInterruptFrame(ctx, queryFd, p, settings.RetryDelayMs)
		if readErr != nil {
			state.lastError = readErr

			return nil, featureAttemptStatusStop
		}

		hadInterruptFrame := false
		if interruptFrame != nil {
			state.lastFrame = interruptFrame
			hadInterruptFrame = true
		}

		interruptReading := decodeInterruptFrame(interruptFrame, queryCandidate, p)
		if interruptReading != nil {
			return interruptReading, featureAttemptStatusSuccess
		}

		if hadInterruptFrame {
			state.setErrorCode(FeatureProbeErrorInterruptFrameInvalid)

			return nil, featureAttemptStatusRetry
		}
	}

	updateFeatureAttemptError(allowInterruptFallback, featureFrame, state)

	return nil, featureAttemptStatusRetry
}
