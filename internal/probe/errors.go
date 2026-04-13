//go:build linux

package probe

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	probeErrorFeatureReadEPIPE    = "feature_read_epipe"
	probeErrorFeatureFrameInvalid = "feature_frame_invalid"
	probeErrorInterruptInvalid    = "interrupt_frame_invalid"
	probeErrorNoValidFrame        = "no_valid_frame"
)

// FeatureProbeErrorCode classifies probe failure reasons.
type FeatureProbeErrorCode uint8

const (
	// FeatureProbeErrorNone indicates no error.
	FeatureProbeErrorNone FeatureProbeErrorCode = iota
	// FeatureProbeErrorReadEPIPE indicates the feature read returned EPIPE.
	FeatureProbeErrorReadEPIPE
	// FeatureProbeErrorFrameInvalid indicates a frame was received but did not match the expected signature.
	FeatureProbeErrorFrameInvalid
	// FeatureProbeErrorInterruptFrameInvalid indicates an interrupt frame was received but did not decode to a reading.
	FeatureProbeErrorInterruptFrameInvalid
	// FeatureProbeErrorNoValidFrame indicates no valid frame was received within the retry budget.
	FeatureProbeErrorNoValidFrame
	// FeatureProbeErrorMessage indicates a probe error with an ad-hoc message.
	FeatureProbeErrorMessage
)

// FeatureProbeError is a typed error carrying a FeatureProbeErrorCode and optional message.
type FeatureProbeError struct {
	Code    FeatureProbeErrorCode
	Message string
}

// ErrorCode returns a stable machine-readable probe failure code when available.
func (e *FeatureProbeError) ErrorCode() string {
	if e == nil {
		return ""
	}

	switch e.Code {
	case FeatureProbeErrorNone:
		return ""
	case FeatureProbeErrorReadEPIPE:
		return probeErrorFeatureReadEPIPE
	case FeatureProbeErrorFrameInvalid:
		return probeErrorFeatureFrameInvalid
	case FeatureProbeErrorInterruptFrameInvalid:
		return probeErrorInterruptInvalid
	case FeatureProbeErrorNoValidFrame:
		return probeErrorNoValidFrame
	case FeatureProbeErrorMessage:
		return ""
	default:
		return ""
	}
}

// Error returns a human-readable description of the probe error.
func (e *FeatureProbeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}

	return e.ErrorCode()
}

func featureProbeErrorf(msg string) error {
	return &FeatureProbeError{
		Code:    FeatureProbeErrorMessage,
		Message: msg,
	}
}

func newFeatureProbeErrorCode(code FeatureProbeErrorCode) error {
	return &FeatureProbeError{Code: code}
}

func formatOSError(err error) string {
	var errno unix.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("%s (errno=%d)", errno.Error(), errno)
	}

	return err.Error()
}

func isEPIPE(err error) bool {
	return errors.Is(err, unix.EPIPE)
}
