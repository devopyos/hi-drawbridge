//go:build linux

package polling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

const warnEveryNFailures = 10

var errNilFetchDevices = errors.New("fetchDevices must not be nil")

// SnapshotStatus reports health and timing diagnostics for the polling loop.
type SnapshotStatus struct {
	LastRefreshAt       time.Time
	LastSuccessAt       time.Time
	ConsecutiveFailures int
	LastError           error
}

// PollingDeviceSnapshot periodically fetches device data and exposes the latest snapshot.
type PollingDeviceSnapshot struct {
	fetchDevices func(context.Context) ([]model.BatteryDevice, error)
	interval     time.Duration
	logger       *slog.Logger

	mu                  sync.RWMutex
	refreshCh           chan struct{}
	latestDevices       []model.BatteryDevice
	lastRefreshAt       time.Time
	lastSuccessAt       time.Time
	consecutiveFailures int
	lastError           error
}

// NewPollingDeviceSnapshot creates a snapshot refresher that calls fetchDevices at the given interval.
func NewPollingDeviceSnapshot(
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	intervalSec int,
	logger *slog.Logger,
) (*PollingDeviceSnapshot, error) {
	if fetchDevices == nil {
		return nil, errNilFetchDevices
	}
	if intervalSec <= 0 {
		return nil, fmt.Errorf("intervalSec must be > 0, got %d", intervalSec)
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &PollingDeviceSnapshot{
		fetchDevices: fetchDevices,
		interval:     time.Duration(intervalSec) * time.Second,
		logger:       logger,
		refreshCh:    make(chan struct{}, 1),
	}, nil
}

// GetLatestDevices returns a copy of the most recently fetched device list.
func (s *PollingDeviceSnapshot) GetLatestDevices() []model.BatteryDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.BatteryDevice, len(s.latestDevices))
	copy(out, s.latestDevices)
	return out
}

// Status returns snapshot refresh diagnostics.
func (s *PollingDeviceSnapshot) Status() SnapshotStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return SnapshotStatus{
		LastRefreshAt:       s.lastRefreshAt,
		LastSuccessAt:       s.lastSuccessAt,
		ConsecutiveFailures: s.consecutiveFailures,
		LastError:           s.lastError,
	}
}

// RefreshEvents returns a coalesced channel signaled after successful refreshes.
// At most one signal is queued; consumers should call GetLatestDevices to read current state.
// The channel is never closed.
func (s *PollingDeviceSnapshot) RefreshEvents() <-chan struct{} { return s.refreshCh }

// Run starts the polling loop, blocking until the context is cancelled.
func (s *PollingDeviceSnapshot) Run(ctx context.Context) error {
	if s.interval <= 0 {
		return fmt.Errorf("polling interval must be > 0, got %s", s.interval)
	}

	if s.safeRefresh(ctx) {
		s.signalRefresh()
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if s.safeRefresh(ctx) {
				s.signalRefresh()
			}
		}
	}
}

func (s *PollingDeviceSnapshot) safeRefresh(ctx context.Context) bool {
	now := time.Now()
	devices, err := s.fetchDevices(ctx)
	if err != nil {
		failures := s.recordRefreshFailure(now, err)
		s.logRefreshFailure(failures, err, len(devices))
		if len(devices) == 0 {
			return false
		}
	} else {
		recovered, previousFailures := s.recordRefreshSuccess(now)
		if recovered {
			s.logger.Info(
				"polling refresh recovered",
				"previous_consecutive_failures", previousFailures,
				"device_count", len(devices),
			)
		}
	}

	s.mu.Lock()
	changed := !slices.Equal(s.latestDevices, devices)
	if changed {
		s.latestDevices = make([]model.BatteryDevice, len(devices))
		copy(s.latestDevices, devices)
	}
	s.mu.Unlock()
	return changed
}

func (s *PollingDeviceSnapshot) recordRefreshFailure(now time.Time, err error) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastRefreshAt = now
	s.lastError = err
	s.consecutiveFailures++

	return s.consecutiveFailures
}

func (s *PollingDeviceSnapshot) recordRefreshSuccess(now time.Time) (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	previousFailures := s.consecutiveFailures
	s.lastRefreshAt = now
	s.lastSuccessAt = now
	s.lastError = nil
	s.consecutiveFailures = 0

	return previousFailures > 0, previousFailures
}

func (s *PollingDeviceSnapshot) logRefreshFailure(failures int, err error, deviceCount int) {
	if deviceCount > 0 {
		s.logger.Warn(
			"polling refresh returned devices with errors",
			"error", err,
			"device_count", deviceCount,
			"consecutive_failures", failures,
		)
		return
	}

	if failures == 1 || failures%warnEveryNFailures == 0 {
		s.logger.Warn(
			"polling refresh failed",
			"error", err,
			"consecutive_failures", failures,
		)
		return
	}

	s.logger.Debug(
		"polling refresh failed",
		"error", err,
		"consecutive_failures", failures,
	)
}

func (s *PollingDeviceSnapshot) signalRefresh() {
	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}
