//go:build linux

package probe

import (
	"errors"
	"log/slog"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/hidio"
)

var probeOpen = unix.Open

var probeClose = unix.Close

var probeWrite = unix.Write

var probePoll = unix.Poll

var probeRead = unix.Read

type fdCloseTracker struct {
	queryFd     int
	wakeFd      int
	queryClosed bool
	wakeClosed  bool
}

func (t *fdCloseTracker) markIfClosed(err error, fd int) {
	if !errors.Is(err, hidio.ErrFdClosed) {
		return
	}
	if fd == t.queryFd {
		t.queryClosed = true
	}
	if fd == t.wakeFd {
		t.wakeClosed = true
	}
}

func closeFD(fd int, path string, logger *slog.Logger) {
	if err := probeClose(fd); err != nil {
		if logger != nil {
			logger.Debug("failed to close hidraw fd", "path", path, "error", formatOSError(err))
		}
	}
}

func isRetryablePollErr(err error) bool {
	return errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN)
}

func isRetryableReadErr(err error) bool {
	return errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK)
}
