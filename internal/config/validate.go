//go:build linux

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

// ValidateCoreSettings checks non-D-Bus settings for internal consistency.
func ValidateCoreSettings(s Settings) error {
	if s.StaleTTLSec < s.CacheTTLSec {
		return fmt.Errorf("stale_ttl_sec (%d) must be greater than or equal to cache_ttl_sec (%d)", s.StaleTTLSec, s.CacheTTLSec)
	}

	if s.ForceHidraw != nil {
		if err := validateForceHidraw(*s.ForceHidraw); err != nil {
			return err
		}
	}

	return nil
}

// ValidateDBusSettings checks that D-Bus name, path, and interface are syntactically valid.
func ValidateDBusSettings(s Settings) error {
	if !isValidBusName(s.DBusBusName) {
		return fmt.Errorf("invalid dbus bus name %q", s.DBusBusName)
	}

	if !dbus.ObjectPath(s.DBusObjectPath).IsValid() {
		return fmt.Errorf("invalid dbus object path %q", s.DBusObjectPath)
	}

	if !isValidInterfaceName(s.DBusInterfaceName) {
		return fmt.Errorf("invalid dbus interface name %q", s.DBusInterfaceName)
	}

	return nil
}

func validateForceHidraw(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return errors.New("force_hidraw must not be empty")
	}

	if filepath.Base(trimmed) == "" || !strings.HasPrefix(filepath.Base(trimmed), "hidraw") {
		return fmt.Errorf("force_hidraw must point to a hidraw device path, got %q", trimmed)
	}

	info, err := os.Lstat(trimmed)
	if err != nil {
		return fmt.Errorf("force_hidraw %q is invalid: %w", trimmed, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("force_hidraw %q must not be a symlink", trimmed)
	}

	if info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("force_hidraw %q must be a character device", trimmed)
	}

	return nil
}

type nameValidationOptions struct {
	allowHyphen       bool
	rejectColonPrefix bool
	rejectTrailingDot bool
}

func isValidDotSeparatedName(value string, opts nameValidationOptions) bool {
	if len(value) == 0 || len(value) > 255 || strings.HasPrefix(value, ".") {
		return false
	}

	if opts.rejectColonPrefix && strings.HasPrefix(value, ":") {
		return false
	}

	if opts.rejectTrailingDot && strings.HasSuffix(value, ".") {
		return false
	}

	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}

	charOK := isInterfaceNameChar
	if opts.allowHyphen {
		charOK = isBusNameChar
	}

	for _, part := range parts {
		if !isValidNamePart(part, charOK) {
			return false
		}
	}

	return true
}

func isValidNamePart(part string, charOK func(rune) bool) bool {
	if len(part) == 0 || (part[0] >= '0' && part[0] <= '9') {
		return false
	}

	for _, c := range part {
		if !charOK(c) {
			return false
		}
	}

	return true
}

func isValidBusName(value string) bool {
	return isValidDotSeparatedName(value, nameValidationOptions{
		allowHyphen:       true,
		rejectColonPrefix: true,
		rejectTrailingDot: true,
	})
}

func isValidInterfaceName(value string) bool {
	return isValidDotSeparatedName(value, nameValidationOptions{})
}

func isBusNameChar(c rune) bool {
	return isInterfaceNameChar(c) || c == '-'
}

func isInterfaceNameChar(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

// DefaultUserConfigPath returns the default path for the user YAML config file.
func DefaultUserConfigPath() string {
	xdgConfigHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdgConfigHome != "" {
		return filepath.Join(xdgConfigHome, "hi-drawbridge", "config.yaml")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "hi-drawbridge", "config.yaml")
	}

	return filepath.Join(home, ".config", "hi-drawbridge", "config.yaml")
}
