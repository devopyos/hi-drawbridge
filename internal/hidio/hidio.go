//go:build linux

package hidio

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/profile"
)

const (
	iocNRBits   = 8
	iocTypeBits = 8
	iocSizeBits = 14
	iocDirBits  = 2

	iocNRShift   = 0
	iocTypeShift = iocNRBits
	iocSizeShift = iocTypeShift + iocTypeBits
	iocDirShift  = iocSizeShift + iocSizeBits

	iocWrite      = 0x01
	iocRead       = 0x02
	hidrawIOCType = 'H'

	maxHIDReportLength   = 256
	maxInterruptReadSize = 256
	writePollIntervalMs  = 25
)

var hidPoll = unix.Poll

var hidRead = unix.Read

var hidWrite = unix.Write

var ioctlWithContextNFn = ioctlWithContextN

func ioc(dir, typ, nr, size uintptr) uintptr {
	return (dir << iocDirShift) | (typ << iocTypeShift) | (nr << iocNRShift) | (size << iocSizeShift)
}

// HidSetFeatureRequest returns the ioctl request code for HID_SET_FEATURE with the given payload length.
func HidSetFeatureRequest(length int) uintptr {
	//nolint:gosec // ioctl request size expects uintptr; caller controls and bounds payload length.
	return ioc(iocWrite|iocRead, hidrawIOCType, 0x06, uintptr(length))
}

// HidGetFeatureRequest returns the ioctl request code for HID_GET_FEATURE with the given buffer length.
func HidGetFeatureRequest(length int) uintptr {
	//nolint:gosec // ioctl request size expects uintptr; caller controls and bounds payload length.
	return ioc(iocWrite|iocRead, hidrawIOCType, 0x07, uintptr(length))
}

// SendFeatureReport sends a HID feature report with the given report ID and payload.
func SendFeatureReport(ctx context.Context, fd int, reportID byte, payload []byte) ([]byte, error) {
	totalLen := 1 + len(payload)
	if err := validateReportLength("feature report", totalLen); err != nil {
		return nil, err
	}

	buf := make([]byte, 0, 1+len(payload))
	buf = append(buf, reportID)
	buf = append(buf, payload...)

	n, err := ioctlWithContextNFn(ctx, fd, HidSetFeatureRequest(len(buf)), uintptr(unsafe.Pointer(&buf[0]))) //nolint:gosec // G103: unavoidable for hidraw ioctl
	runtime.KeepAlive(buf)
	if err != nil {
		return nil, fmt.Errorf("HID_SET_FEATURE ioctl: %w", err)
	}
	if n > len(buf) {
		return nil, fmt.Errorf("HID_SET_FEATURE ioctl: returned %d bytes for %d-byte payload", n, len(buf))
	}
	if n > 0 && n < len(buf) {
		return nil, fmt.Errorf("HID_SET_FEATURE ioctl: short transfer, wrote %d of %d bytes", n, len(buf))
	}

	return buf, nil
}

// RecvFeatureReport receives a HID feature report by issuing HID_GET_FEATURE and reading the response.
func RecvFeatureReport(ctx context.Context, fd int, reportID byte, totalLen int) ([]byte, error) {
	if err := validateReportLength("feature report receive", totalLen); err != nil {
		return nil, err
	}

	buf := make([]byte, totalLen)
	buf[0] = reportID

	n, err := ioctlWithContextNFn(ctx, fd, HidGetFeatureRequest(totalLen), uintptr(unsafe.Pointer(&buf[0]))) //nolint:gosec // G103: unavoidable for hidraw ioctl
	runtime.KeepAlive(buf)
	if err != nil {
		return nil, fmt.Errorf("HID_GET_FEATURE ioctl: %w", err)
	}
	if n <= 0 {
		return nil, fmt.Errorf("HID_GET_FEATURE ioctl: returned %d bytes", n)
	}
	if n > len(buf) {
		return nil, fmt.Errorf("HID_GET_FEATURE ioctl: returned %d bytes for %d-byte buffer", n, len(buf))
	}

	return buf[:n], nil
}

// SendQuery constructs and sends a feature report query for a battery command using the profile's parameters.
func SendQuery(ctx context.Context, fd, cmd int, p profile.ProfileSpec) ([]byte, error) {
	if err := validateByteValue("query_report_id", p.QueryReportID); err != nil {
		return nil, err
	}
	if err := validateByteValue("command", cmd); err != nil {
		return nil, err
	}

	if p.QueryLength > maxHIDReportLength {
		return nil, fmt.Errorf("query_length must be <= %d", maxHIDReportLength)
	}

	payloadLen := p.QueryLength - 1
	if payloadLen < 1 {
		return nil, errors.New("query_length must be at least 2 for feature query")
	}

	payload := make([]byte, payloadLen)
	//nolint:gosec // cmd is validated as a byte during profile config loading.
	payload[0] = byte(cmd)

	//nolint:gosec // query_report_id is validated as a byte during profile config loading.
	return SendFeatureReport(ctx, fd, byte(p.QueryReportID), payload)
}

// SendOutputQuery writes a battery query as an output report to the hidraw device.
func SendOutputQuery(ctx context.Context, fd, cmd int, p profile.ProfileSpec) (int, error) {
	if err := validateByteValue("query_report_id", p.QueryReportID); err != nil {
		return 0, err
	}
	if err := validateByteValue("command", cmd); err != nil {
		return 0, err
	}

	if p.QueryLength > maxHIDReportLength {
		return 0, fmt.Errorf("query_length must be <= %d", maxHIDReportLength)
	}

	payloadLen := p.QueryLength - 1
	if payloadLen < 2 {
		return 0, errors.New("query_length must be at least 3 for output query")
	}

	payload := make([]byte, payloadLen)
	//nolint:gosec // query_report_id is validated as a byte during profile config loading.
	payload[0] = byte(p.QueryReportID)
	//nolint:gosec // cmd is validated as a byte during profile config loading.
	payload[1] = byte(cmd)

	n, err := writeAllWithContext(ctx, fd, payload)
	if err != nil {
		return n, err
	}

	if n != len(payload) {
		return n, fmt.Errorf("short output write: wrote %d of %d bytes", n, len(payload))
	}

	return n, nil
}

// IsValidInterruptFrame checks whether a raw interrupt frame matches the profile's expected report ID and command.
func IsValidInterruptFrame(frame []byte, p profile.ProfileSpec) bool {
	if p.FallbackInputReportID < 0 || p.FallbackInputReportID > 255 {
		return false
	}
	if p.FallbackInputCmd < 0 || p.FallbackInputCmd > 255 {
		return false
	}

	minLength := max(2, p.FallbackInputLength)
	if len(frame) < minLength {
		return false
	}

	if frame[0] != byte(p.FallbackInputReportID) {
		return false
	}

	return frame[1] == byte(p.FallbackInputCmd)
}

// ReadInterruptFrame polls the hidraw fd for a valid interrupt frame within a timeout derived from retryDelayMs.
//
//nolint:gocyclo // Polling and retry handling keep this loop intentionally explicit.
func ReadInterruptFrame(ctx context.Context, fd int, p profile.ProfileSpec, retryDelayMs int) ([]byte, error) {
	timeoutMs := max(120, retryDelayMs*6)
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	pollInterval := time.Duration(max(20, retryDelayMs*2)) * time.Millisecond

	var latestValid []byte
	readBuf := make([]byte, maxInterruptReadSize)

	//nolint:gosec // fd is provided by unix.Open and is a non-negative OS file descriptor.
	pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLERR | unix.POLLHUP | unix.POLLNVAL}}

	for {
		if err := ctx.Err(); err != nil {
			return latestValid, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		interval := pollInterval
		interval = min(interval, remaining)

		pollTimeout := max(1, int(interval.Milliseconds()))

		n, err := hidPoll(pollFds, pollTimeout)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}

			return latestValid, fmt.Errorf("poll interrupt frame: %w", err)
		}

		if n == 0 {
			continue
		}

		if pollFds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return latestValid, fmt.Errorf("hidraw poll: POLLERR, POLLHUP, or POLLNVAL on fd %d", fd)
		}

		for range n {
			nRead, readErr := hidRead(fd, readBuf)
			if readErr != nil {
				if errors.Is(readErr, unix.EINTR) || errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EWOULDBLOCK) {
					continue
				}

				return latestValid, fmt.Errorf("read interrupt frame: %w", readErr)
			}

			if nRead <= 0 {
				continue
			}

			frame := readBuf[:nRead]
			if IsValidInterruptFrame(frame, p) {
				latestValid = append(latestValid[:0], frame...)
			}
		}
	}

	return latestValid, nil
}

func validateReportLength(field string, length int) error {
	if length < 1 {
		return fmt.Errorf("%s length must be >= 1", field)
	}

	if length > maxHIDReportLength {
		return fmt.Errorf("%s length must be <= %d", field, maxHIDReportLength)
	}

	return nil
}

func validateByteValue(field string, value int) error {
	if value < 0 || value > 255 {
		return fmt.Errorf("%s must be in range [0..255]", field)
	}

	return nil
}

func writeAllWithContext(ctx context.Context, fd int, payload []byte) (int, error) {
	written := 0
	for written < len(payload) {
		if err := ctx.Err(); err != nil {
			return written, err
		}

		n, err := hidWrite(fd, payload[written:])
		if n > 0 {
			written += n
		}

		if err == nil {
			continue
		}

		if errors.Is(err, unix.EINTR) {
			continue
		}

		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if waitErr := waitUntilWritable(ctx, fd); waitErr != nil {
				return written, waitErr
			}

			continue
		}

		return written, err
	}

	return written, nil
}

func waitUntilWritable(ctx context.Context, fd int) error {
	//nolint:gosec // fd is provided by unix.Open and is a non-negative OS file descriptor.
	pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT | unix.POLLERR | unix.POLLHUP | unix.POLLNVAL}}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, err := hidPoll(pollFds, writePollIntervalMs)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}

			return fmt.Errorf("poll output query writable: %w", err)
		}

		if n == 0 {
			continue
		}

		if pollFds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return fmt.Errorf("output write poll: POLLERR, POLLHUP, or POLLNVAL on fd %d", fd)
		}

		if pollFds[0].Revents&unix.POLLOUT != 0 {
			return nil
		}
	}
}
