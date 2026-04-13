//go:build linux

package hidio

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestIoctlWithContextCancelReturnsErrFdClosedPromptly(t *testing.T) {
	origIoctl := ioctlSyscall
	origClose := closeSyscall
	blocked := make(chan struct{})
	release := make(chan struct{})
	ioctlSyscall = func(fd int, op, arg uintptr) (uintptr, uintptr, unix.Errno) {
		close(blocked)
		<-release
		return 0, 0, 0
	}
	closeSyscall = func(fd int) error {
		close(release)
		return nil
	}
	defer func() {
		ioctlSyscall = origIoctl
		closeSyscall = origClose
	}()

	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatalf("eventfd: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ioctlWithContext(ctx, fd, 0, 0)
	}()

	select {
	case <-blocked:
	case <-time.After(200 * time.Millisecond):
		close(release)
		t.Fatal("timeout waiting for ioctl to start")
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrFdClosed) {
			t.Fatalf("expected ErrFdClosed, got %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation to be preserved, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ioctlWithContext did not return promptly on cancel")
	}
}

func TestIoctlWithContextCancelBeforeStart(t *testing.T) {
	origIoctl := ioctlSyscall
	ioctlSyscall = func(fd int, op, arg uintptr) (uintptr, uintptr, unix.Errno) {
		t.Fatal("ioctl syscall should not run when context is already canceled")
		return 0, 0, 0
	}
	defer func() {
		ioctlSyscall = origIoctl
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ioctlWithContext(ctx, 1, 0, 0)
	if err == nil {
		t.Fatal("expected cancellation error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestIoctlWithContextNReturnsSyscallError(t *testing.T) {
	origIoctl := ioctlSyscall
	defer func() {
		ioctlSyscall = origIoctl
	}()

	ioctlSyscall = func(fd int, op, arg uintptr) (uintptr, uintptr, unix.Errno) {
		return 0, 0, unix.EPERM
	}

	_, err := ioctlWithContextN(context.Background(), 10, 0x1234, 0)
	if err == nil {
		t.Fatal("expected syscall error")
	}

	if !errors.Is(err, unix.EPERM) {
		t.Fatalf("expected EPERM, got %v", err)
	}
}

func TestIoctlWithContextNWrapsCancelWhenSyscallReturnsError(t *testing.T) {
	origIoctl := ioctlSyscall
	defer func() {
		ioctlSyscall = origIoctl
	}()

	ctx, cancel := context.WithCancel(context.Background())
	ioctlSyscall = func(fd int, op, arg uintptr) (uintptr, uintptr, unix.Errno) {
		cancel()
		return 0, 0, unix.EBADF
	}

	_, err := ioctlWithContextN(ctx, 10, 0x1234, 0)
	if err == nil {
		t.Fatal("expected cancellation-wrapped error")
	}

	if !errors.Is(err, ErrFdClosed) {
		t.Fatalf("expected ErrFdClosed, got %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if !errors.Is(err, unix.EBADF) {
		t.Fatalf("expected EBADF to be preserved, got %v", err)
	}
}

func TestIoctlWithContextNWrapsCancelWhenSyscallSucceedsAfterCancel(t *testing.T) {
	origIoctl := ioctlSyscall
	defer func() {
		ioctlSyscall = origIoctl
	}()

	ctx, cancel := context.WithCancel(context.Background())
	ioctlSyscall = func(fd int, op, arg uintptr) (uintptr, uintptr, unix.Errno) {
		cancel()
		return 7, 0, 0
	}

	n, err := ioctlWithContextN(ctx, 10, 0x1234, 0)
	if n != 7 {
		t.Fatalf("expected n=7, got %d", n)
	}

	if err == nil {
		t.Fatal("expected cancellation-wrapped error")
	}

	if !errors.Is(err, ErrFdClosed) {
		t.Fatalf("expected ErrFdClosed, got %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
