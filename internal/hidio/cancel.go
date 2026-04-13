//go:build linux

package hidio

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

var ErrFdClosed = errors.New("fd closed to cancel blocking ioctl")

var ioctlSyscall = func(fd int, op, arg uintptr) (uintptr, uintptr, unix.Errno) {
	return unix.Syscall(unix.SYS_IOCTL, uintptr(fd), op, arg)
}

var closeSyscall = unix.Close

// ioctlWithContext executes an ioctl call with context cancellation support by closing the fd on cancel.
func ioctlWithContext(ctx context.Context, fd int, op, arg uintptr) error {
	_, err := ioctlWithContextN(ctx, fd, op, arg)

	return err
}

func ioctlWithContextN(ctx context.Context, fd int, op, arg uintptr) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("ioctl cancelled before start: %w", err)
	}

	stopCancel := make(chan struct{})
	defer close(stopCancel)

	go func() {
		select {
		case <-ctx.Done():
			_ = closeSyscall(fd)
		case <-stopCancel:
			return
		}
	}()

	r1, _, errno := ioctlSyscall(fd, op, arg)
	if errno != 0 {
		sysErr := fmt.Errorf("ioctl(%d, 0x%x): %w", fd, op, errno)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, errors.Join(ErrFdClosed, ctxErr, sysErr)
		}

		return 0, sysErr
	}

	n, err := ioctlResultInt(r1)
	if err != nil {
		return 0, err
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return n, errors.Join(ErrFdClosed, ctxErr)
	}

	return n, nil
}

func ioctlResultInt(v uintptr) (int, error) {
	maxInt := ^uint(0) >> 1
	if v > uintptr(maxInt) {
		return 0, fmt.Errorf("ioctl returned out-of-range result %d", v)
	}

	return int(v), nil //nolint:gosec // G115: v is bounded above by maxInt immediately above.
}
