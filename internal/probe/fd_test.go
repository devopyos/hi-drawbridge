//go:build linux

package probe

import (
	"errors"
	"log/slog"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/hidio"
)

func TestFdCloseTrackerMarkIfClosed(t *testing.T) {
	tracker := &fdCloseTracker{
		queryFd: 10,
		wakeFd:  11,
	}

	tracker.markIfClosed(errors.New("other"), 10)
	if tracker.queryClosed || tracker.wakeClosed {
		t.Fatal("expected unrelated error not to mark fds as closed")
	}

	tracker.markIfClosed(hidio.ErrFdClosed, 10)
	if !tracker.queryClosed || tracker.wakeClosed {
		t.Fatalf("expected only query fd marked closed, got query=%v wake=%v", tracker.queryClosed, tracker.wakeClosed)
	}

	tracker.markIfClosed(hidio.ErrFdClosed, 11)
	if !tracker.wakeClosed {
		t.Fatal("expected wake fd marked closed")
	}
}

func TestCloseFDUsesProbeCloseAndToleratesNilLogger(t *testing.T) {
	origClose := probeClose
	defer func() {
		probeClose = origClose
	}()

	closeCalls := 0
	probeClose = func(fd int) error {
		closeCalls++
		return unix.EBADF
	}

	closeFD(42, "/dev/hidraw42", nil)
	if closeCalls != 1 {
		t.Fatalf("expected one close call, got %d", closeCalls)
	}

	logger := slog.New(slog.DiscardHandler)
	closeFD(43, "/dev/hidraw43", logger)
	if closeCalls != 2 {
		t.Fatalf("expected second close call, got %d", closeCalls)
	}
}

func TestRetryableErrHelpers(t *testing.T) {
	if !isRetryablePollErr(unix.EINTR) || !isRetryablePollErr(unix.EAGAIN) {
		t.Fatal("expected EINTR/EAGAIN poll errors to be retryable")
	}
	if isRetryablePollErr(unix.EIO) {
		t.Fatal("expected EIO poll error not retryable")
	}

	if !isRetryableReadErr(unix.EINTR) || !isRetryableReadErr(unix.EAGAIN) || !isRetryableReadErr(unix.EWOULDBLOCK) {
		t.Fatal("expected EINTR/EAGAIN/EWOULDBLOCK read errors to be retryable")
	}
	if isRetryableReadErr(unix.EIO) {
		t.Fatal("expected EIO read error not retryable")
	}
}
