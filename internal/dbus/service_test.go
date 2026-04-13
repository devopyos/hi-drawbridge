//go:build linux

package dbus

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newTestService(
	t *testing.T,
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
) *Service {
	t.Helper()

	service, err := NewService(
		"com.example.BatteryWatch",
		"/com/example/BatteryWatch",
		"com.example.BatteryWatch",
		fetchDevices,
		discardLogger(),
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	return service
}

func TestNewServiceRejectsNilFetchDevices(t *testing.T) {
	service, err := NewService(
		"com.example.BatteryWatch",
		"/com/example/BatteryWatch",
		"com.example.BatteryWatch",
		nil,
		discardLogger(),
	)
	if !errors.Is(err, errNilFetchDevices) {
		t.Fatalf("expected errNilFetchDevices, got %v", err)
	}
	if service != nil {
		t.Fatalf("expected nil service when fetchDevices is nil")
	}
}

func TestNewServiceDefaultsLogger(t *testing.T) {
	service, err := NewService(
		"com.example.BatteryWatch",
		"/com/example/BatteryWatch",
		"com.example.BatteryWatch",
		func(context.Context) ([]model.BatteryDevice, error) { return nil, nil },
		nil,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if service.logger == nil {
		t.Fatalf("expected default logger")
	}
}

func TestGetDevicesSuccessCachesPayload(t *testing.T) {
	devices := []model.BatteryDevice{{
		ID:         "device-1",
		Name:       "Keyboard",
		DeviceType: "keyboard",
		IconName:   "input-keyboard",
		Percentage: 88,
		IsCharging: true,
	}}

	expected, err := devicesToJSON(devices)
	if err != nil {
		t.Fatalf("failed to build expected json: %v", err)
	}

	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return devices, nil
	})
	service.minInterval = time.Hour

	api := &companionAPI{service: service}
	payload, dbusErr := api.GetDevices()
	if dbusErr != nil {
		t.Fatalf("expected no error, got %v", dbusErr)
	}
	if payload != expected {
		t.Fatalf("expected payload %q, got %q", expected, payload)
	}
	if service.cachedJSON != expected {
		t.Fatalf("expected cached payload %q, got %q", expected, service.cachedJSON)
	}
	if service.lastCall.IsZero() {
		t.Fatalf("expected lastCall to be set")
	}
	if service.lastAttempt.IsZero() {
		t.Fatalf("expected lastAttempt to be set")
	}
}

func TestGetDevicesFetchFailurePreservesCache(t *testing.T) {
	cache := "cached-payload"
	lastCall := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	interfaceName := "com.example.BatteryWatch"

	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, errors.New("fetch failed")
	})
	service.cachedJSON = cache
	service.lastCall = lastCall
	service.minInterval = time.Minute

	api := &companionAPI{service: service}
	payload, dbusErr := api.GetDevices()
	if dbusErr == nil {
		t.Fatalf("expected fetch error")
	}
	if payload != "" {
		t.Fatalf("expected empty payload, got %q", payload)
	}
	if dbusErr.Name != interfaceName+".FetchFailed" {
		t.Fatalf("expected error name %q, got %q", interfaceName+".FetchFailed", dbusErr.Name)
	}
	if service.cachedJSON != cache {
		t.Fatalf("expected cached payload %q, got %q", cache, service.cachedJSON)
	}
	if !service.lastCall.Equal(lastCall) {
		t.Fatalf("expected lastCall %v, got %v", lastCall, service.lastCall)
	}
}

func TestGetDevicesFetchFailureWithEmptyCache(t *testing.T) {
	interfaceName := "com.example.BatteryWatch"
	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, errors.New("fetch failed")
	})
	service.minInterval = time.Minute

	api := &companionAPI{service: service}
	payload, dbusErr := api.GetDevices()
	if dbusErr == nil {
		t.Fatalf("expected fetch error")
	}
	if payload != "" {
		t.Fatalf("expected empty payload, got %q", payload)
	}
	if dbusErr.Name != interfaceName+".FetchFailed" {
		t.Fatalf("expected error name %q, got %q", interfaceName+".FetchFailed", dbusErr.Name)
	}
	if service.cachedJSON != "" {
		t.Fatalf("expected empty cache, got %q", service.cachedJSON)
	}
	if !service.lastCall.IsZero() {
		t.Fatalf("expected lastCall to remain zero, got %v", service.lastCall)
	}
	if service.lastAttempt.IsZero() {
		t.Fatalf("expected lastAttempt to be set after fetch failure")
	}
}

func TestGetDevicesFetchFailureNotifiesWaiters(t *testing.T) {
	interfaceName := "com.example.BatteryWatch"
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32

	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return nil, errors.New("fetch failed")
	})

	api := &companionAPI{service: service}
	var wg sync.WaitGroup
	results := make(chan *dbus.Error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := api.GetDevices()
		results <- err
	}()

	<-started

	go func() {
		defer wg.Done()
		_, err := api.GetDevices()
		results <- err
	}()

	close(release)
	wg.Wait()
	close(results)

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected one fetch call, got %d", calls)
	}

	for err := range results {
		if err == nil {
			t.Fatalf("expected fetch error")
		}
		if err.Name != interfaceName+".FetchFailed" {
			t.Fatalf("expected error name %q, got %q", interfaceName+".FetchFailed", err.Name)
		}
	}
}

func TestGetDevicesFetchFailureIsThrottled(t *testing.T) {
	interfaceName := "com.example.BatteryWatch"
	var calls int32

	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("fetch failed")
	})
	service.minInterval = time.Hour

	api := &companionAPI{service: service}

	_, firstErr := api.GetDevices()
	_, secondErr := api.GetDevices()

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected one fetch call during throttle interval, got %d", calls)
	}
	if firstErr == nil || secondErr == nil {
		t.Fatalf("expected fetch errors on both calls")
	}
	if firstErr.Name != interfaceName+".FetchFailed" || secondErr.Name != interfaceName+".FetchFailed" {
		t.Fatalf("unexpected error names: first=%q second=%q", firstErr.Name, secondErr.Name)
	}
}

type fakeDBusConn struct {
	exportErr    error
	requestErr   error
	releaseErr   error
	closeErr     error
	requestReply dbus.RequestNameReply
	releaseReply dbus.ReleaseNameReply

	exportCalled   bool
	requestCalled  bool
	releaseCalls   int
	closeCalls     int
	exportedPath   dbus.ObjectPath
	exportedIface  string
	requestedName  string
	requestedFlags dbus.RequestNameFlags
	releasedName   string
	requestedCh    chan struct{}
}

func (f *fakeDBusConn) Export(_ any, path dbus.ObjectPath, iface string) error {
	f.exportCalled = true
	f.exportedPath = path
	f.exportedIface = iface
	return f.exportErr
}

func (f *fakeDBusConn) RequestName(name string, flags dbus.RequestNameFlags) (dbus.RequestNameReply, error) {
	f.requestCalled = true
	f.requestedName = name
	f.requestedFlags = flags
	if f.requestedCh != nil {
		close(f.requestedCh)
	}
	return f.requestReply, f.requestErr
}

func (f *fakeDBusConn) ReleaseName(name string) (dbus.ReleaseNameReply, error) {
	f.releaseCalls++
	f.releasedName = name
	return f.releaseReply, f.releaseErr
}

func (f *fakeDBusConn) Close() error {
	f.closeCalls++
	return f.closeErr
}

func TestRunSessionBusFailure(t *testing.T) {
	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, nil
	})
	service.openSessionBus = func() (dbusConnection, error) {
		return nil, errors.New("connect failed")
	}

	err := service.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "failed to connect to session bus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunExportFailureClosesConnection(t *testing.T) {
	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, nil
	})
	conn := &fakeDBusConn{exportErr: errors.New("export failed")}
	service.openSessionBus = func() (dbusConnection, error) {
		return conn, nil
	}

	err := service.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "failed to export object") {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("expected one Close() call, got %d", conn.closeCalls)
	}
	if conn.releaseCalls != 0 {
		t.Fatalf("expected no ReleaseName() call, got %d", conn.releaseCalls)
	}
}

func TestRunRequestNameFailureClosesConnection(t *testing.T) {
	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, nil
	})
	conn := &fakeDBusConn{requestErr: errors.New("request failed")}
	service.openSessionBus = func() (dbusConnection, error) {
		return conn, nil
	}

	err := service.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "failed to request bus name") {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("expected one Close() call, got %d", conn.closeCalls)
	}
	if conn.releaseCalls != 0 {
		t.Fatalf("expected no ReleaseName() call, got %d", conn.releaseCalls)
	}
}

func TestRunNameAlreadyTaken(t *testing.T) {
	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, nil
	})
	conn := &fakeDBusConn{requestReply: dbus.RequestNameReplyExists}
	service.openSessionBus = func() (dbusConnection, error) {
		return conn, nil
	}

	err := service.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "name already taken") {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("expected one Close() call, got %d", conn.closeCalls)
	}
	if conn.releaseCalls != 0 {
		t.Fatalf("expected no ReleaseName() call, got %d", conn.releaseCalls)
	}
}

func TestRunSuccessReleasesNameOnCancel(t *testing.T) {
	service := newTestService(t, func(context.Context) ([]model.BatteryDevice, error) {
		return nil, nil
	})
	conn := &fakeDBusConn{
		requestReply: dbus.RequestNameReplyPrimaryOwner,
		releaseReply: dbus.ReleaseNameReplyReleased,
		requestedCh:  make(chan struct{}),
	}
	service.openSessionBus = func() (dbusConnection, error) {
		return conn, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- service.Run(ctx)
	}()

	select {
	case <-conn.requestedCh:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for RequestName()")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Run() to return")
	}

	if !conn.exportCalled {
		t.Fatalf("expected Export() to be called")
	}
	if conn.exportedPath != service.objectPath {
		t.Fatalf("expected export path %q, got %q", service.objectPath, conn.exportedPath)
	}
	if conn.exportedIface != service.interfaceName {
		t.Fatalf("expected export interface %q, got %q", service.interfaceName, conn.exportedIface)
	}
	if !conn.requestCalled {
		t.Fatalf("expected RequestName() to be called")
	}
	if conn.requestedName != service.busName {
		t.Fatalf("expected RequestName() bus %q, got %q", service.busName, conn.requestedName)
	}
	if conn.requestedFlags != dbus.NameFlagDoNotQueue {
		t.Fatalf("expected RequestName() flags %d, got %d", dbus.NameFlagDoNotQueue, conn.requestedFlags)
	}
	if conn.releaseCalls != 1 {
		t.Fatalf("expected one ReleaseName() call, got %d", conn.releaseCalls)
	}
	if conn.releasedName != service.busName {
		t.Fatalf("expected ReleaseName() bus %q, got %q", service.busName, conn.releasedName)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("expected one Close() call, got %d", conn.closeCalls)
	}
}
