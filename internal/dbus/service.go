//go:build linux

package dbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

var errNilFetchDevices = errors.New("fetchDevices must not be nil")

type dbusConnection interface {
	Export(v any, path dbus.ObjectPath, iface string) error
	RequestName(name string, flags dbus.RequestNameFlags) (dbus.RequestNameReply, error)
	ReleaseName(name string) (dbus.ReleaseNameReply, error)
	Close() error
}

type companionDevice struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	DeviceType string             `json:"device_type"`
	IconName   string             `json:"icon_name"`
	Batteries  []companionBattery `json:"batteries"`
}

type companionBattery struct {
	Percentage int  `json:"percentage"`
	Charging   bool `json:"charging"`
}

func devicesToJSON(devices []model.BatteryDevice) (string, error) {
	companionDevices := make([]companionDevice, 0, len(devices))
	for _, d := range devices {
		companionDevices = append(companionDevices, companionDevice{
			ID:         d.ID,
			Name:       d.Name,
			DeviceType: d.DeviceType,
			IconName:   d.IconName,
			Batteries:  []companionBattery{{Percentage: d.Percentage, Charging: d.IsCharging}},
		})
	}

	data, err := json.Marshal(companionDevices)
	if err != nil {
		return "", fmt.Errorf("marshal companion devices payload: %w", err)
	}

	return string(data), nil
}

// Service exposes battery device data over the session D-Bus using the BatteryWatch companion API.
type Service struct {
	busName        string
	objectPath     dbus.ObjectPath
	interfaceName  string
	fetchDevices   func(context.Context) ([]model.BatteryDevice, error)
	openSessionBus func() (dbusConnection, error)
	logger         *slog.Logger
	ctx            context.Context //nolint:containedctx // needed because D-Bus handler has fixed signature

	mu          sync.Mutex
	lastCall    time.Time
	lastAttempt time.Time
	minInterval time.Duration
	cachedJSON  string
	refreshCond *sync.Cond
	refreshing  bool
	refreshID   uint64
	lastDoneID  uint64
	refreshErr  *dbus.Error
}

const defaultThrottleInterval = 500 * time.Millisecond

// NewService creates a D-Bus service that fetches devices through the provided callback.
func NewService(
	busName string,
	objectPath string,
	interfaceName string,
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	logger *slog.Logger,
) (*Service, error) {
	if fetchDevices == nil {
		return nil, errNilFetchDevices
	}
	if logger == nil {
		logger = slog.Default()
	}

	service := &Service{
		busName:       busName,
		objectPath:    dbus.ObjectPath(objectPath),
		interfaceName: interfaceName,
		fetchDevices:  fetchDevices,
		openSessionBus: func() (dbusConnection, error) {
			return dbus.SessionBus()
		},
		logger:      logger,
		minInterval: defaultThrottleInterval,
	}
	service.refreshCond = sync.NewCond(&service.mu)
	return service, nil
}

type companionAPI struct {
	service *Service
}

func (c *companionAPI) GetDevices() (string, *dbus.Error) {
	s := c.service

	s.mu.Lock()
	if s.refreshCond == nil {
		s.refreshCond = sync.NewCond(&s.mu)
	}
	var waitedID uint64
	for {
		now := time.Now()
		if !s.refreshing {
			if waitedID != 0 && s.lastDoneID == waitedID && s.refreshErr != nil {
				err := s.refreshErr
				s.mu.Unlock()
				return "", err
			}
			if now.Sub(s.lastAttempt) < s.minInterval {
				if s.refreshErr != nil {
					err := s.refreshErr
					s.mu.Unlock()
					return "", err
				}

				if s.cachedJSON != "" {
					cached := s.cachedJSON
					s.mu.Unlock()
					return cached, nil
				}
			}
			s.refreshing = true
			s.refreshID++
			currentID := s.refreshID
			s.refreshErr = nil
			ctx := s.ctx
			s.mu.Unlock()

			if ctx == nil {
				ctx = context.Background()
			}
			devices, err := s.fetchDevices(ctx)
			if err != nil {
				s.logger.Debug("D-Bus GetDevices fetch error", "error", err)
				refreshErr := dbus.NewError(
					s.interfaceName+".FetchFailed",
					[]any{"failed to fetch devices"},
				)
				s.mu.Lock()
				s.refreshErr = refreshErr
				s.lastAttempt = time.Now()
				s.lastDoneID = currentID
				s.refreshing = false
				s.refreshCond.Broadcast()
				s.mu.Unlock()
				return "", refreshErr
			}

			payload, err := devicesToJSON(devices)
			if err != nil {
				s.logger.Error("D-Bus GetDevices serialization failed", "error", err, "device_count", len(devices))
				refreshErr := dbus.NewError(
					s.interfaceName+".SerializationFailed",
					[]any{"failed to serialize devices response"},
				)
				s.mu.Lock()
				s.refreshErr = refreshErr
				s.lastAttempt = time.Now()
				s.lastDoneID = currentID
				s.refreshing = false
				s.refreshCond.Broadcast()
				s.mu.Unlock()
				return "", refreshErr
			}

			now = time.Now()
			s.mu.Lock()
			s.cachedJSON = payload
			s.lastCall = now
			s.lastAttempt = now
			s.lastDoneID = currentID
			s.refreshing = false
			s.refreshCond.Broadcast()
			s.mu.Unlock()

			return payload, nil
		}

		waitedID = s.refreshID
		s.refreshCond.Wait()
	}
}

// Run connects to the session bus, exports the companion API, and blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	if s.logger == nil {
		s.logger = slog.Default()
	}

	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	conn, err := s.openSessionBus()
	if err != nil {
		return fmt.Errorf("failed to connect to session bus: %w", err)
	}

	acquiredName := false
	defer func() {
		if acquiredName {
			if _, releaseErr := conn.ReleaseName(s.busName); releaseErr != nil {
				s.logger.Debug("failed to release D-Bus name", "bus_name", s.busName, "error", releaseErr)
			}
		}

		if closeErr := conn.Close(); closeErr != nil {
			s.logger.Debug("failed to close D-Bus connection", "error", closeErr)
		}
	}()

	api := &companionAPI{service: s}

	err = conn.Export(api, s.objectPath, s.interfaceName)
	if err != nil {
		return fmt.Errorf("failed to export object: %w", err)
	}

	reply, err := conn.RequestName(s.busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("failed to request bus name: %w", err)
	}

	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("failed to acquire bus name %s: name already taken", s.busName)
	}

	acquiredName = true

	s.logger.Info("D-Bus service registered", "bus_name", s.busName, "object_path", string(s.objectPath))

	<-ctx.Done()
	s.logger.Info("D-Bus service stopping", "reason", ctx.Err())

	return nil
}
