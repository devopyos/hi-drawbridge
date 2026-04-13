//go:build linux

package polling

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

func newTestSnapshot(
	t *testing.T,
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	intervalSec int,
	logger *slog.Logger,
) *PollingDeviceSnapshot {
	t.Helper()

	snapshot, err := NewPollingDeviceSnapshot(fetchDevices, intervalSec, logger)
	if err != nil {
		t.Fatalf("NewPollingDeviceSnapshot() error = %v", err)
	}

	return snapshot
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestSafeRefreshSuccessUpdatesCache(t *testing.T) {
	devices := []model.BatteryDevice{{
		ID:         "device-1",
		Name:       "Keyboard",
		DeviceType: "keyboard",
		IconName:   "input-keyboard",
		Percentage: 88,
		IsCharging: true,
	}}

	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) {
			return devices, nil
		},
		1,
		discardLogger(),
	)

	if changed := snapshot.safeRefresh(context.Background()); !changed {
		t.Fatalf("expected refresh to report change")
	}

	latest := snapshot.GetLatestDevices()
	if len(latest) != 1 || latest[0].ID != devices[0].ID {
		t.Fatalf("expected cached devices to update, got %#v", latest)
	}
}

func TestSafeRefreshPartialFailureCachesDevices(t *testing.T) {
	devices := []model.BatteryDevice{{
		ID:         "device-2",
		Name:       "Mouse",
		DeviceType: "mouse",
		IconName:   "input-mouse",
		Percentage: 55,
		IsCharging: false,
	}}

	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) {
			return devices, errors.New("partial failure")
		},
		1,
		discardLogger(),
	)
	snapshot.latestDevices = []model.BatteryDevice{{ID: "stale"}}

	if changed := snapshot.safeRefresh(context.Background()); !changed {
		t.Fatalf("expected refresh to report change on partial success")
	}

	latest := snapshot.GetLatestDevices()
	if len(latest) != 1 || latest[0].ID != devices[0].ID {
		t.Fatalf("expected cached devices to update on partial success, got %#v", latest)
	}
}

func TestSafeRefreshFullFailureKeepsCache(t *testing.T) {
	stale := []model.BatteryDevice{{
		ID:         "device-stale",
		Name:       "Headset",
		DeviceType: "headset",
		IconName:   "audio-headphones",
		Percentage: 40,
		IsCharging: false,
	}}

	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) {
			return nil, errors.New("fetch failed")
		},
		1,
		discardLogger(),
	)
	snapshot.latestDevices = stale

	if changed := snapshot.safeRefresh(context.Background()); changed {
		t.Fatalf("expected refresh to report no change on full failure")
	}

	latest := snapshot.GetLatestDevices()
	if len(latest) != 1 || latest[0].ID != stale[0].ID {
		t.Fatalf("expected cached devices to remain unchanged, got %#v", latest)
	}
}

func TestRunRefreshSignalsOnlyOnChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devicesA := []model.BatteryDevice{{ID: "device-a"}}
	devicesB := []model.BatteryDevice{{ID: "device-b"}}
	var calls int32

	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) {
			call := atomic.AddInt32(&calls, 1)
			switch call {
			case 1:
				return devicesA, nil
			case 2:
				return devicesA, nil
			case 3:
				cancel()
				return devicesB, nil
			default:
				return devicesB, nil
			}
		},
		1,
		discardLogger(),
	)
	snapshot.interval = 10 * time.Millisecond
	snapshot.refreshCh = make(chan struct{}, 10)

	var refreshes int32
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-snapshot.RefreshEvents():
				atomic.AddInt32(&refreshes, 1)
			case <-done:
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- snapshot.Run(ctx)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected Run to exit cleanly, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Run to exit")
	}

	close(done)
	wg.Wait()

	if refreshes != 2 {
		t.Fatalf("expected 2 refresh signals, got %d", refreshes)
	}
}

func TestNewPollingDeviceSnapshotValidatesInputs(t *testing.T) {
	logger := discardLogger()

	_, err := NewPollingDeviceSnapshot(nil, 1, logger)
	if !errors.Is(err, errNilFetchDevices) {
		t.Fatalf("expected errNilFetchDevices, got %v", err)
	}

	_, err = NewPollingDeviceSnapshot(
		func(context.Context) ([]model.BatteryDevice, error) { return nil, nil },
		0,
		logger,
	)
	if err == nil {
		t.Fatal("expected interval validation error")
	}

	snapshot, err := NewPollingDeviceSnapshot(
		func(context.Context) ([]model.BatteryDevice, error) { return nil, nil },
		1,
		nil,
	)
	if err != nil {
		t.Fatalf("expected nil logger to be accepted, got %v", err)
	}
	if snapshot.logger == nil {
		t.Fatal("expected logger fallback to be configured")
	}
}

func TestStatusTracksFailureAndRecovery(t *testing.T) {
	var calls int
	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("transient failure")
			}

			return []model.BatteryDevice{{ID: "ok"}}, nil
		},
		1,
		discardLogger(),
	)

	if changed := snapshot.safeRefresh(context.Background()); changed {
		t.Fatal("expected first refresh (full failure) to report no change")
	}

	failed := snapshot.Status()
	if failed.ConsecutiveFailures != 1 {
		t.Fatalf("expected 1 consecutive failure, got %d", failed.ConsecutiveFailures)
	}
	if failed.LastError == nil {
		t.Fatal("expected LastError to be populated after failure")
	}
	if failed.LastRefreshAt.IsZero() {
		t.Fatal("expected LastRefreshAt to be set after failure")
	}
	if !failed.LastSuccessAt.IsZero() {
		t.Fatalf("expected zero LastSuccessAt before any successful refresh, got %s", failed.LastSuccessAt)
	}

	if changed := snapshot.safeRefresh(context.Background()); !changed {
		t.Fatal("expected recovery refresh to report change")
	}

	recovered := snapshot.Status()
	if recovered.ConsecutiveFailures != 0 {
		t.Fatalf("expected failure counter reset, got %d", recovered.ConsecutiveFailures)
	}
	if recovered.LastError != nil {
		t.Fatalf("expected LastError to be cleared, got %v", recovered.LastError)
	}
	if recovered.LastSuccessAt.IsZero() {
		t.Fatal("expected LastSuccessAt to be set after recovery")
	}
	if recovered.LastRefreshAt.Before(failed.LastRefreshAt) {
		t.Fatalf("expected LastRefreshAt to move forward, failed=%s recovered=%s", failed.LastRefreshAt, recovered.LastRefreshAt)
	}
}

func TestRefreshEventsAreCoalesced(t *testing.T) {
	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) { return nil, nil },
		1,
		discardLogger(),
	)

	snapshot.signalRefresh()
	snapshot.signalRefresh()

	select {
	case <-snapshot.RefreshEvents():
	default:
		t.Fatal("expected one refresh event")
	}

	select {
	case <-snapshot.RefreshEvents():
		t.Fatal("expected second refresh signal to be coalesced")
	default:
	}
}

func TestRunRejectsNonPositiveInterval(t *testing.T) {
	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) { return nil, nil },
		1,
		discardLogger(),
	)
	snapshot.interval = 0

	err := snapshot.Run(context.Background())
	if err == nil {
		t.Fatal("expected Run to reject non-positive interval")
	}
}

type recordedLogEntry struct {
	level slog.Level
	msg   string
}

type recordingHandler struct {
	mu      sync.Mutex
	records []recordedLogEntry
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, recordedLogEntry{
		level: record.Level,
		msg:   record.Message,
	})
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

func (h *recordingHandler) levels() []slog.Level {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]slog.Level, len(h.records))
	for i, r := range h.records {
		out[i] = r.level
	}

	return out
}

func TestSafeRefreshLogsFailureAndRecovery(t *testing.T) {
	handler := &recordingHandler{}
	logger := slog.New(handler)

	var calls int
	snapshot := newTestSnapshot(
		t,
		func(context.Context) ([]model.BatteryDevice, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("fail")
			}

			return []model.BatteryDevice{{ID: "device"}}, nil
		},
		1,
		logger,
	)

	snapshot.safeRefresh(context.Background())
	snapshot.safeRefresh(context.Background())

	levels := handler.levels()
	if !slices.Contains(levels, slog.LevelWarn) {
		t.Fatalf("expected warning log for failure, got levels=%v", levels)
	}
	if !slices.Contains(levels, slog.LevelInfo) {
		t.Fatalf("expected info log for recovery, got levels=%v", levels)
	}
}
