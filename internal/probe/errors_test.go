//go:build linux

package probe

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFeatureProbeErrorError(t *testing.T) {
	var nilErr *FeatureProbeError
	if nilErr.Error() != "" {
		t.Fatalf("expected nil FeatureProbeError string to be empty, got %q", nilErr.Error())
	}

	tests := []struct {
		name string
		err  *FeatureProbeError
		want string
	}{
		{
			name: "message overrides code",
			err:  &FeatureProbeError{Code: FeatureProbeErrorReadEPIPE, Message: "custom"},
			want: "custom",
		},
		{
			name: "read epipe",
			err:  &FeatureProbeError{Code: FeatureProbeErrorReadEPIPE},
			want: probeErrorFeatureReadEPIPE,
		},
		{
			name: "feature frame invalid",
			err:  &FeatureProbeError{Code: FeatureProbeErrorFrameInvalid},
			want: probeErrorFeatureFrameInvalid,
		},
		{
			name: "interrupt frame invalid",
			err:  &FeatureProbeError{Code: FeatureProbeErrorInterruptFrameInvalid},
			want: probeErrorInterruptInvalid,
		},
		{
			name: "no valid frame",
			err:  &FeatureProbeError{Code: FeatureProbeErrorNoValidFrame},
			want: probeErrorNoValidFrame,
		},
		{
			name: "none",
			err:  &FeatureProbeError{Code: FeatureProbeErrorNone},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorHelpers(t *testing.T) {
	err := featureProbeErrorf("boom")
	typed := &FeatureProbeError{}
	if !errors.As(err, &typed) {
		t.Fatalf("expected featureProbeErrorf to return *FeatureProbeError, got %T", err)
	}
	if typed.Code != FeatureProbeErrorMessage || typed.Message != "boom" {
		t.Fatalf("unexpected featureProbeErrorf payload: %+v", typed)
	}

	err = newFeatureProbeErrorCode(FeatureProbeErrorInterruptFrameInvalid)
	typed = &FeatureProbeError{}
	if !errors.As(err, &typed) {
		t.Fatalf("expected newFeatureProbeErrorCode to return *FeatureProbeError, got %T", err)
	}
	if typed.Code != FeatureProbeErrorInterruptFrameInvalid {
		t.Fatalf("unexpected code: %+v", typed)
	}
}

func TestFormatOSErrorAndIsEPIPE(t *testing.T) {
	if got := formatOSError(unix.EBADF); got == unix.EBADF.Error() {
		t.Fatalf("expected formatted errno with code, got %q", got)
	}

	if got := formatOSError(errors.New("custom")); got != "custom" {
		t.Fatalf("expected passthrough string, got %q", got)
	}

	if !isEPIPE(unix.EPIPE) {
		t.Fatal("expected isEPIPE(unix.EPIPE) to be true")
	}
	if isEPIPE(unix.EIO) {
		t.Fatal("expected isEPIPE(unix.EIO) to be false")
	}
}
