//go:build linux

package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

const envPrefix = "HI_DRAWBRIDGE_"

// Settings holds fully resolved runtime configuration values.
type Settings struct {
	PreferredTransports    []model.Transport `json:"preferred_transports"`
	ForceHidraw            *string           `json:"force_hidraw"`
	RetryCount             int               `json:"retry_count"`
	RetryDelayMs           int               `json:"retry_delay_ms"`
	CacheTTLSec            int               `json:"cache_ttl_sec"`
	StaleTTLSec            int               `json:"stale_ttl_sec"`
	ServicePollIntervalSec int               `json:"service_poll_interval_sec"`
	LogLevel               string            `json:"log_level"`
	DBusBusName            string            `json:"dbus_bus_name"`
	DBusObjectPath         string            `json:"dbus_object_path"`
	DBusInterfaceName      string            `json:"dbus_interface_name"`
}

// Overrides contains optional configuration values that override defaults when set.
type Overrides struct {
	PreferredTransports    *string
	ForceHidraw            *string
	RetryCount             *int
	RetryDelayMs           *int
	CacheTTLSec            *int
	StaleTTLSec            *int
	ServicePollIntervalSec *int
	LogLevel               *string
	DBusBusName            *string
	DBusObjectPath         *string
	DBusInterfaceName      *string
}

// DefaultSettings returns the built-in default configuration values.
func DefaultSettings() Settings {
	return Settings{
		PreferredTransports:    []model.Transport{model.TransportUSBDirect, model.TransportReceiver},
		RetryCount:             6,
		RetryDelayMs:           30,
		CacheTTLSec:            30,
		StaleTTLSec:            600,
		ServicePollIntervalSec: 10,
		LogLevel:               "INFO",
		DBusBusName:            "org.batterywatch.Companion",
		DBusObjectPath:         "/org/batterywatch/Companion",
		DBusInterfaceName:      "org.batterywatch.Companion",
	}
}

// ResolveSettings builds final Settings by layering file, environment, and CLI overrides onto defaults.
func ResolveSettings(fileOverrides, cliOverrides *Overrides) (Settings, error) {
	s := DefaultSettings()

	if fileOverrides != nil {
		if err := applyOverrides(&s, fileOverrides); err != nil {
			return Settings{}, fmt.Errorf("invalid file overrides: %w", err)
		}
	}

	envOverrides, err := loadEnvOverrides()
	if err != nil {
		return Settings{}, err
	}

	if err := applyOverrides(&s, &envOverrides); err != nil {
		return Settings{}, fmt.Errorf("invalid env overrides: %w", err)
	}

	if cliOverrides != nil {
		if err := applyOverrides(&s, cliOverrides); err != nil {
			return Settings{}, fmt.Errorf("invalid cli overrides: %w", err)
		}
	}

	return s, nil
}

func loadEnvOverrides() (Overrides, error) {
	var o Overrides

	lookupEnv := func(name string) (string, string, bool) {
		envName := envPrefix + name
		if v := os.Getenv(envName); v != "" {
			return v, envName, true
		}
		return "", "", false
	}

	envFields := []struct {
		name string
		dest **string
	}{
		{"preferred_transports", &o.PreferredTransports},
		{"force_hidraw", &o.ForceHidraw},
		{"log_level", &o.LogLevel},
		{"dbus_bus_name", &o.DBusBusName},
		{"dbus_object_path", &o.DBusObjectPath},
		{"dbus_interface_name", &o.DBusInterfaceName},
	}

	for _, f := range envFields {
		if v, _, ok := lookupEnv(f.name); ok {
			*f.dest = &v
		}
	}

	intFields := []struct {
		name string
		dest **int
	}{
		{"retry_count", &o.RetryCount},
		{"retry_delay_ms", &o.RetryDelayMs},
		{"cache_ttl_sec", &o.CacheTTLSec},
		{"stale_ttl_sec", &o.StaleTTLSec},
		{"service_poll_interval_sec", &o.ServicePollIntervalSec},
	}

	for _, f := range intFields {
		v, envName, ok := lookupEnv(f.name)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return Overrides{}, fmt.Errorf("%s must be a valid integer, got %q", envName, v)
		}

		*f.dest = &n
	}

	return o, nil
}

func applyOverrides(s *Settings, o *Overrides) error {
	if err := applyPreferredTransportsOverride(&s.PreferredTransports, o.PreferredTransports); err != nil {
		return err
	}

	applyForceHidrawOverride(&s.ForceHidraw, o.ForceHidraw)

	if err := applyBoundedIntOverride(&s.RetryCount, o.RetryCount, "retry_count", 1, 50); err != nil {
		return err
	}

	if err := applyBoundedIntOverride(&s.RetryDelayMs, o.RetryDelayMs, "retry_delay_ms", 1, 5000); err != nil {
		return err
	}

	if err := applyBoundedIntOverride(&s.CacheTTLSec, o.CacheTTLSec, "cache_ttl_sec", 1, 3600); err != nil {
		return err
	}

	if err := applyBoundedIntOverride(&s.StaleTTLSec, o.StaleTTLSec, "stale_ttl_sec", 1, 86400); err != nil {
		return err
	}

	if err := applyBoundedIntOverride(&s.ServicePollIntervalSec, o.ServicePollIntervalSec, "service_poll_interval_sec", 1, 3600); err != nil {
		return err
	}

	if err := applyLogLevelOverride(&s.LogLevel, o.LogLevel); err != nil {
		return err
	}

	applyStringOverride(&s.DBusBusName, o.DBusBusName)
	applyStringOverride(&s.DBusObjectPath, o.DBusObjectPath)
	applyStringOverride(&s.DBusInterfaceName, o.DBusInterfaceName)

	return nil
}

func applyPreferredTransportsOverride(dest *[]model.Transport, override *string) error {
	if override == nil {
		return nil
	}

	transports, err := parseTransports(*override)
	if err != nil {
		return fmt.Errorf("preferred_transports: %w", err)
	}

	*dest = transports
	return nil
}

func applyForceHidrawOverride(dest **string, override *string) {
	if override == nil {
		return
	}

	trimmed := strings.TrimSpace(*override)
	if trimmed == "" {
		*dest = nil
		return
	}

	*dest = &trimmed
}

func applyBoundedIntOverride(dest, override *int, name string, lower, upper int) error {
	if override == nil {
		return nil
	}

	if *override < lower || *override > upper {
		return fmt.Errorf("%s must be in range [%d..%d], got %d", name, lower, upper, *override)
	}

	*dest = *override
	return nil
}

func applyLogLevelOverride(dest, override *string) error {
	if override == nil {
		return nil
	}

	normalized := strings.ToUpper(strings.TrimSpace(*override))
	if !isValidLogLevel(normalized) {
		return errors.New("log_level must be one of DEBUG, INFO, WARNING, ERROR")
	}

	*dest = normalized
	return nil
}

func applyStringOverride(dest, override *string) {
	if override == nil {
		return
	}

	*dest = *override
}

func parseTransports(value string) ([]model.Transport, error) {
	parts := strings.Split(value, ",")

	seen := make(map[model.Transport]bool)
	var result []model.Transport

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		t, err := model.ParseTransport(trimmed)
		if err != nil {
			return nil, err
		}

		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}

	if len(result) == 0 {
		return nil, errors.New("preferred_transports must not be empty")
	}

	return result, nil
}

func isValidLogLevel(level string) bool {
	switch level {
	case "DEBUG", "INFO", "WARNING", "ERROR":
		return true
	default:
		return false
	}
}

// TransportRank returns a map from transport to its priority index based on PreferredTransports order.
func TransportRank(s Settings) map[model.Transport]int {
	result := make(map[model.Transport]int, len(s.PreferredTransports))
	for i, t := range s.PreferredTransports {
		result[t] = i
	}

	return result
}
