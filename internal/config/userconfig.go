//go:build linux

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// LoadedUserConfig holds the result of loading and parsing a user YAML config file.
type LoadedUserConfig struct {
	Path            string
	Exists          bool
	BridgeOverrides *Overrides
}

const maxUserConfigFileBytes int64 = 1 << 20

type yamlConfig struct {
	Bridge   *yamlOverrides `yaml:"bridge,omitempty"`
	Profiles any            `yaml:"profiles,omitempty"`
}

type yamlOverrides struct {
	PreferredTransports    *string `yaml:"preferred_transports,omitempty"`
	ForceHidraw            *string `yaml:"force_hidraw,omitempty"`
	RetryCount             *int    `yaml:"retry_count,omitempty"`
	RetryDelayMs           *int    `yaml:"retry_delay_ms,omitempty"`
	CacheTTLSec            *int    `yaml:"cache_ttl_sec,omitempty"`
	StaleTTLSec            *int    `yaml:"stale_ttl_sec,omitempty"`
	ServicePollIntervalSec *int    `yaml:"service_poll_interval_sec,omitempty"`
	LogLevel               *string `yaml:"log_level,omitempty"`
	DBusBusName            *string `yaml:"dbus_bus_name,omitempty"`
	DBusObjectPath         *string `yaml:"dbus_object_path,omitempty"`
	DBusInterfaceName      *string `yaml:"dbus_interface_name,omitempty"`
}

// ToOverrides converts the YAML overrides into a config.Overrides.
func (y *yamlOverrides) ToOverrides() *Overrides {
	return &Overrides{
		PreferredTransports:    y.PreferredTransports,
		ForceHidraw:            y.ForceHidraw,
		RetryCount:             y.RetryCount,
		RetryDelayMs:           y.RetryDelayMs,
		CacheTTLSec:            y.CacheTTLSec,
		StaleTTLSec:            y.StaleTTLSec,
		ServicePollIntervalSec: y.ServicePollIntervalSec,
		LogLevel:               y.LogLevel,
		DBusBusName:            y.DBusBusName,
		DBusObjectPath:         y.DBusObjectPath,
		DBusInterfaceName:      y.DBusInterfaceName,
	}
}

// LoadUserConfig reads and parses the user YAML config file at the given path, or the default path if nil.
func LoadUserConfig(path *string) (*LoadedUserConfig, error) {
	resolvedPath := DefaultUserConfigPath()
	if path != nil {
		resolvedPath = *path
	}

	_, err := os.Lstat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &LoadedUserConfig{
				Path:            resolvedPath,
				Exists:          false,
				BridgeOverrides: &Overrides{},
			}, nil
		}

		return nil, fmt.Errorf("failed to stat config file %s: %w", resolvedPath, err)
	}

	data, err := readConfigFile(resolvedPath)
	if err != nil {
		return nil, err
	}

	cfg, err := decodeUserConfigYAML(resolvedPath, data)
	if err != nil {
		return nil, err
	}
	if cfg.Profiles != nil {
		return nil, fmt.Errorf(
			"config file %s: profiles are not supported in config.yaml; use the embedded profile catalog or place local profile YAMLs under ~/.config/hi-drawbridge/profiles (or override via --profiles-dir / HI_DRAWBRIDGE_profiles_dir)",
			resolvedPath,
		)
	}

	bridgeOverrides := &Overrides{}
	if cfg.Bridge != nil {
		bridgeOverrides = cfg.Bridge.ToOverrides()
	}

	return &LoadedUserConfig{
		Path:            resolvedPath,
		Exists:          true,
		BridgeOverrides: bridgeOverrides,
	}, nil
}

func readConfigFile(path string) (data []byte, err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("config path %s must not be a symlink", path)
		}

		return nil, fmt.Errorf("failed to open config file %s: %w", path, err)
	}

	defer func() {
		if closeErr := unix.Close(fd); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close config file %s: %w", path, closeErr)
		}
	}()

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("failed to stat config file %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("config path %s must be a regular file", path)
	}
	if st.Size > maxUserConfigFileBytes {
		return nil, fmt.Errorf("config file %s exceeds max size %d bytes", path, maxUserConfigFileBytes)
	}

	buf := make([]byte, 0, 8192)
	chunk := make([]byte, 4096)
	for {
		n, readErr := unix.Read(fd, chunk)
		if readErr != nil {
			if errors.Is(readErr, unix.EINTR) {
				continue
			}

			return nil, fmt.Errorf("failed to read config file %s: %w", path, readErr)
		}
		if n == 0 {
			break
		}
		if int64(len(buf)+n) > maxUserConfigFileBytes {
			return nil, fmt.Errorf("config file %s exceeds max size %d bytes", path, maxUserConfigFileBytes)
		}

		buf = append(buf, chunk[:n]...)
	}

	return buf, nil
}

func decodeUserConfigYAML(path string, data []byte) (yamlConfig, error) {
	var cfg yamlConfig

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	decodeErr := decoder.Decode(&cfg)
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		return yamlConfig{}, fmt.Errorf("failed to parse config file %s: %w", path, decodeErr)
	}

	if decodeErr == nil {
		var trailingDoc any
		trailingErr := decoder.Decode(&trailingDoc)
		if trailingErr == nil {
			return yamlConfig{}, fmt.Errorf("failed to parse config file %s: multiple YAML documents are not supported", path)
		}
		if !errors.Is(trailingErr, io.EOF) {
			return yamlConfig{}, fmt.Errorf("failed to parse config file %s: %w", path, trailingErr)
		}
	}

	return cfg, nil
}
