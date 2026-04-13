//go:build linux

package hidio

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func TestSendFeatureReportBuildsBufferAndIssuesIoctl(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	var gotReq uintptr
	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		gotReq = op
		return 4, nil
	}

	buf, err := SendFeatureReport(context.Background(), 10, 0x51, []byte{0x06, 0x00, 0x00})
	if err != nil {
		t.Fatalf("SendFeatureReport error: %v", err)
	}

	expected := []byte{0x51, 0x06, 0x00, 0x00}
	if !bytes.Equal(buf, expected) {
		t.Fatalf("expected %v, got %v", expected, buf)
	}

	if gotReq != HidSetFeatureRequest(len(expected)) {
		t.Fatalf("unexpected ioctl request: got 0x%x want 0x%x", gotReq, HidSetFeatureRequest(len(expected)))
	}
}

func TestSendFeatureReportRejectsShortIoctlTransfer(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		return 1, nil
	}

	_, err := SendFeatureReport(context.Background(), 10, 0x51, []byte{0x06})
	if err == nil {
		t.Fatal("expected short-transfer error")
	}

	if !strings.Contains(err.Error(), "short transfer") {
		t.Fatalf("expected short-transfer error, got %v", err)
	}
}

func TestSendFeatureReportRejectsOversizedIoctlTransfer(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		return 5, nil
	}

	_, err := SendFeatureReport(context.Background(), 10, 0x51, []byte{0x06, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected oversized-transfer error")
	}

	if !strings.Contains(err.Error(), "returned 5 bytes") {
		t.Fatalf("expected oversized-transfer error, got %v", err)
	}
}

func TestSendFeatureReportValidatesLength(t *testing.T) {
	payload := bytes.Repeat([]byte{0x00}, maxHIDReportLength)
	_, err := SendFeatureReport(context.Background(), 10, 0x51, payload)
	if err == nil {
		t.Fatal("expected report-length validation error")
	}

	if !strings.Contains(err.Error(), "length must be <=") {
		t.Fatalf("expected length validation error, got %v", err)
	}
}

func TestRecvFeatureReportUsesReturnedLength(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	var gotReq uintptr
	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		gotReq = op
		return 3, nil
	}

	frame, err := RecvFeatureReport(context.Background(), 10, 0x51, 6)
	if err != nil {
		t.Fatalf("RecvFeatureReport error: %v", err)
	}

	expected := []byte{0x51, 0x00, 0x00}
	if !bytes.Equal(frame, expected) {
		t.Fatalf("expected %v, got %v", expected, frame)
	}

	if gotReq != HidGetFeatureRequest(6) {
		t.Fatalf("unexpected ioctl request: got 0x%x want 0x%x", gotReq, HidGetFeatureRequest(6))
	}
}

func TestRecvFeatureReportRejectsZeroLengthIoctlResult(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		return 0, nil
	}

	_, err := RecvFeatureReport(context.Background(), 10, 0x51, 6)
	if err == nil {
		t.Fatal("expected error for zero-length ioctl result")
	}

	if !strings.Contains(err.Error(), "returned 0 bytes") {
		t.Fatalf("expected zero-byte error, got %v", err)
	}
}

func TestRecvFeatureReportRejectsOversizedIoctlResult(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		return 7, nil
	}

	_, err := RecvFeatureReport(context.Background(), 10, 0x51, 6)
	if err == nil {
		t.Fatal("expected oversized result error")
	}

	if !strings.Contains(err.Error(), "returned 7 bytes") {
		t.Fatalf("expected oversized result error, got %v", err)
	}
}

func TestRecvFeatureReportValidatesLength(t *testing.T) {
	_, err := RecvFeatureReport(context.Background(), 10, 0x51, 0)
	if err == nil {
		t.Fatal("expected receive-length validation error")
	}

	if !strings.Contains(err.Error(), "length must be >=") {
		t.Fatalf("expected receive-length validation error, got %v", err)
	}
}

func TestSendQueryValidatesByteFields(t *testing.T) {
	base := profile.ProfileSpec{
		QueryLength:   4,
		QueryReportID: 0x51,
	}

	invalidReportID := base
	invalidReportID.QueryReportID = 300
	if _, err := SendQuery(context.Background(), 10, 0x06, invalidReportID); err == nil {
		t.Fatal("expected report-id validation error")
	}

	if _, err := SendQuery(context.Background(), 10, -1, base); err == nil {
		t.Fatal("expected command validation error")
	}
}

func TestSendQueryValidatesLength(t *testing.T) {
	p := profile.ProfileSpec{
		QueryLength:   maxHIDReportLength + 1,
		QueryReportID: 0x51,
	}
	if _, err := SendQuery(context.Background(), 10, 0x06, p); err == nil {
		t.Fatal("expected query_length upper-bound validation error")
	}

	p.QueryLength = 1
	if _, err := SendQuery(context.Background(), 10, 0x06, p); err == nil {
		t.Fatal("expected query_length lower-bound validation error")
	}
}

func TestSendQueryBuildsPayload(t *testing.T) {
	origIoctlWithContextN := ioctlWithContextNFn
	defer func() {
		ioctlWithContextNFn = origIoctlWithContextN
	}()

	var gotReq uintptr
	ioctlWithContextNFn = func(ctx context.Context, fd int, op, arg uintptr) (int, error) {
		gotReq = op
		return 4, nil
	}

	p := profile.ProfileSpec{
		QueryLength:   4,
		QueryReportID: 0x51,
	}

	buf, err := SendQuery(context.Background(), 10, 0x06, p)
	if err != nil {
		t.Fatalf("SendQuery error: %v", err)
	}

	expected := []byte{0x51, 0x06, 0x00, 0x00}
	if !bytes.Equal(buf, expected) {
		t.Fatalf("expected %v, got %v", expected, buf)
	}

	if gotReq != HidSetFeatureRequest(len(expected)) {
		t.Fatalf("unexpected ioctl request: got 0x%x want 0x%x", gotReq, HidSetFeatureRequest(len(expected)))
	}
}

func TestSendOutputQueryHandlesPartialAndTransientWrites(t *testing.T) {
	origWrite := hidWrite
	origPoll := hidPoll
	defer func() {
		hidWrite = origWrite
		hidPoll = origPoll
	}()

	p := profile.ProfileSpec{
		QueryLength:   5,
		QueryReportID: 0x51,
	}
	expected := []byte{0x51, 0x06, 0x00, 0x00}
	var writes [][]byte
	pollCalls := 0

	hidWrite = func(fd int, b []byte) (int, error) {
		writes = append(writes, append([]byte(nil), b...))
		switch len(writes) {
		case 1:
			return 1, nil
		case 2:
			return 0, unix.EINTR
		case 3:
			return 0, unix.EAGAIN
		case 4:
			return len(b), nil
		default:
			t.Fatalf("unexpected write call %d", len(writes))
			return 0, nil
		}
	}

	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		pollCalls++
		fds[0].Revents = unix.POLLOUT
		return 1, nil
	}

	n, err := SendOutputQuery(context.Background(), 10, 0x06, p)
	if err != nil {
		t.Fatalf("SendOutputQuery error: %v", err)
	}

	if n != len(expected) {
		t.Fatalf("expected %d bytes written, got %d", len(expected), n)
	}

	if pollCalls != 1 {
		t.Fatalf("expected 1 poll call, got %d", pollCalls)
	}

	if len(writes) != 4 {
		t.Fatalf("expected 4 write attempts, got %d", len(writes))
	}

	if !bytes.Equal(writes[0], expected) {
		t.Fatalf("first write expected %v, got %v", expected, writes[0])
	}

	remaining := expected[1:]
	if !bytes.Equal(writes[1], remaining) || !bytes.Equal(writes[2], remaining) || !bytes.Equal(writes[3], remaining) {
		t.Fatalf("expected remaining writes to use %v, got %v", remaining, writes[1:])
	}
}

func TestSendOutputQueryHonorsContextCancellation(t *testing.T) {
	origWrite := hidWrite
	origPoll := hidPoll
	defer func() {
		hidWrite = origWrite
		hidPoll = origPoll
	}()

	p := profile.ProfileSpec{
		QueryLength:   5,
		QueryReportID: 0x51,
	}

	ctx, cancel := context.WithCancel(context.Background())
	hidWrite = func(fd int, b []byte) (int, error) {
		return 0, unix.EAGAIN
	}
	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		cancel()
		return 0, nil
	}

	n, err := SendOutputQuery(ctx, 10, 0x06, p)
	if err == nil {
		t.Fatal("expected cancellation error")
	}

	if n != 0 {
		t.Fatalf("expected 0 bytes written on cancellation, got %d", n)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSendOutputQueryReturnsPollErrorFlags(t *testing.T) {
	origWrite := hidWrite
	origPoll := hidPoll
	defer func() {
		hidWrite = origWrite
		hidPoll = origPoll
	}()

	p := profile.ProfileSpec{
		QueryLength:   5,
		QueryReportID: 0x51,
	}

	hidWrite = func(fd int, b []byte) (int, error) {
		return 0, unix.EAGAIN
	}
	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		fds[0].Revents = unix.POLLERR
		return 1, nil
	}

	_, err := SendOutputQuery(context.Background(), 10, 0x06, p)
	if err == nil {
		t.Fatal("expected poll error")
	}

	if !strings.Contains(err.Error(), "POLLERR") {
		t.Fatalf("expected poll-flag error, got %v", err)
	}
}

func TestSendOutputQueryReturnsWriteError(t *testing.T) {
	origWrite := hidWrite
	defer func() {
		hidWrite = origWrite
	}()

	p := profile.ProfileSpec{
		QueryLength:   5,
		QueryReportID: 0x51,
	}

	hidWrite = func(fd int, b []byte) (int, error) {
		return 0, unix.EIO
	}

	_, err := SendOutputQuery(context.Background(), 10, 0x06, p)
	if err == nil {
		t.Fatal("expected write error")
	}

	if !errors.Is(err, unix.EIO) {
		t.Fatalf("expected EIO write error, got %v", err)
	}
}

func TestSendOutputQueryHandlesPollEINTR(t *testing.T) {
	origWrite := hidWrite
	origPoll := hidPoll
	defer func() {
		hidWrite = origWrite
		hidPoll = origPoll
	}()

	p := profile.ProfileSpec{
		QueryLength:   5,
		QueryReportID: 0x51,
	}

	writeCalls := 0
	hidWrite = func(fd int, b []byte) (int, error) {
		writeCalls++
		if writeCalls == 1 {
			return 0, unix.EAGAIN
		}

		return len(b), nil
	}

	pollCalls := 0
	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		pollCalls++
		if pollCalls == 1 {
			return 0, unix.EINTR
		}

		fds[0].Revents = unix.POLLOUT
		return 1, nil
	}

	n, err := SendOutputQuery(context.Background(), 10, 0x06, p)
	if err != nil {
		t.Fatalf("SendOutputQuery error: %v", err)
	}

	if n != 4 {
		t.Fatalf("expected 4 bytes written, got %d", n)
	}
}

func TestSendOutputQueryValidatesLength(t *testing.T) {
	p := profile.ProfileSpec{
		QueryLength:   2,
		QueryReportID: 0x51,
	}
	if _, err := SendOutputQuery(context.Background(), 10, 0x06, p); err == nil {
		t.Fatal("expected output query length validation error")
	}

	p.QueryLength = maxHIDReportLength + 1
	if _, err := SendOutputQuery(context.Background(), 10, 0x06, p); err == nil {
		t.Fatal("expected output query upper-bound validation error")
	}
}

func TestIsValidInterruptFrame(t *testing.T) {
	p := profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}

	valid := []byte{0x54, 0xE4, 0x01, 0x00, 0x00}
	if !IsValidInterruptFrame(valid, p) {
		t.Fatal("expected valid interrupt frame")
	}

	invalid := []byte{0x54, 0x00, 0x01, 0x00, 0x00}
	if IsValidInterruptFrame(invalid, p) {
		t.Fatal("expected invalid interrupt frame")
	}

	p.FallbackInputCmd = 300
	if IsValidInterruptFrame(valid, p) {
		t.Fatal("expected invalid interrupt profile to be rejected")
	}

	p = profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}
	if IsValidInterruptFrame([]byte{0x54, 0xE4}, p) {
		t.Fatal("expected too-short frame to be rejected")
	}
}

func TestReadInterruptFrameCapturesLatestValidFrame(t *testing.T) {
	origPoll := hidPoll
	origRead := hidRead
	defer func() {
		hidPoll = origPoll
		hidRead = origRead
	}()

	p := profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}

	frames := [][]byte{
		{0x54, 0x00, 0x01, 0x00, 0x00}, // invalid cmd
		{0x54, 0xE4, 0x02, 0x00, 0x00}, // valid
	}
	readCalls := 0
	pollCalls := 0

	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		pollCalls++
		if pollCalls <= 2 {
			fds[0].Revents = unix.POLLIN
			return 1, nil
		}

		fds[0].Revents = 0
		return 0, nil
	}

	hidRead = func(fd int, buf []byte) (int, error) {
		if readCalls >= len(frames) {
			t.Fatalf("unexpected read call %d", readCalls+1)
		}

		frame := frames[readCalls]
		readCalls++
		copy(buf, frame)
		return len(frame), nil
	}

	start := time.Now()
	frame, err := ReadInterruptFrame(context.Background(), 10, p, 1)
	if err != nil {
		t.Fatalf("ReadInterruptFrame error: %v", err)
	}

	if !bytes.Equal(frame, frames[1]) {
		t.Fatalf("expected latest valid frame %v, got %v", frames[1], frame)
	}

	// retryDelayMs=1 implies a minimum timeout of 120ms for the interrupt drain window.
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("expected timeout window to run before returning, elapsed %s", time.Since(start))
	}
}

func TestReadInterruptFrameReturnsPollFlagErrors(t *testing.T) {
	origPoll := hidPoll
	defer func() {
		hidPoll = origPoll
	}()

	p := profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}

	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		fds[0].Revents = unix.POLLNVAL
		return 1, nil
	}

	_, err := ReadInterruptFrame(context.Background(), 10, p, 1)
	if err == nil {
		t.Fatal("expected poll-flag error")
	}

	if !strings.Contains(err.Error(), "POLLNVAL") {
		t.Fatalf("expected POLLNVAL error, got %v", err)
	}
}

func TestReadInterruptFrameHandlesPollEINTRAndContextDone(t *testing.T) {
	origPoll := hidPoll
	defer func() {
		hidPoll = origPoll
	}()

	p := profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}

	ctx, cancel := context.WithCancel(context.Background())
	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		cancel()
		return 0, unix.EINTR
	}

	_, err := ReadInterruptFrame(ctx, 10, p, 1)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadInterruptFrameReturnsReadError(t *testing.T) {
	origPoll := hidPoll
	origRead := hidRead
	defer func() {
		hidPoll = origPoll
		hidRead = origRead
	}()

	p := profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}

	hidPoll = func(fds []unix.PollFd, timeout int) (int, error) {
		fds[0].Revents = unix.POLLIN
		return 1, nil
	}

	hidRead = func(fd int, buf []byte) (int, error) {
		return 0, unix.EIO
	}

	_, err := ReadInterruptFrame(context.Background(), 10, p, 1)
	if err == nil {
		t.Fatal("expected read error")
	}

	if !errors.Is(err, unix.EIO) {
		t.Fatalf("expected EIO read error, got %v", err)
	}
}

func TestReadInterruptFrameReturnsContextError(t *testing.T) {
	p := profile.ProfileSpec{
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReadInterruptFrame(ctx, 10, p, 1)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
