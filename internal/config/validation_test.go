//go:build linux

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devopyos/hi-drawbridge/internal/model"
)

func TestParseTransports(t *testing.T) {
	transports, err := parseTransports(" receiver , usb_direct,receiver ")
	if err != nil {
		t.Fatalf("parseTransports() error = %v", err)
	}

	want := []model.Transport{model.TransportReceiver, model.TransportUSBDirect}
	if !reflect.DeepEqual(transports, want) {
		t.Fatalf("parseTransports() = %v, want %v", transports, want)
	}

	if _, err := parseTransports(" "); err == nil {
		t.Fatal("expected empty transports error")
	}
	if _, err := parseTransports("bogus"); err == nil {
		t.Fatal("expected invalid transport error")
	}
}

func TestApplyOverrideHelpers(t *testing.T) {
	transports := []model.Transport{model.TransportUSBDirect}
	if err := applyPreferredTransportsOverride(&transports, strPtr("receiver,usb_direct")); err != nil {
		t.Fatalf("applyPreferredTransportsOverride() error = %v", err)
	}
	if !reflect.DeepEqual(transports, []model.Transport{model.TransportReceiver, model.TransportUSBDirect}) {
		t.Fatalf("unexpected transports %v", transports)
	}

	var force *string
	applyForceHidrawOverride(&force, strPtr(" /dev/hidraw0 "))
	if force == nil || *force != "/dev/hidraw0" {
		t.Fatalf("unexpected force_hidraw override %#v", force)
	}
	applyForceHidrawOverride(&force, strPtr(" "))
	if force != nil {
		t.Fatalf("expected blank force_hidraw to clear override, got %#v", force)
	}

	level := "INFO"
	if err := applyLogLevelOverride(&level, strPtr("debug")); err != nil {
		t.Fatalf("applyLogLevelOverride() error = %v", err)
	}
	if level != "DEBUG" {
		t.Fatalf("unexpected normalized log level %q", level)
	}
	if err := applyLogLevelOverride(&level, strPtr("nope")); err == nil {
		t.Fatal("expected invalid log level error")
	}

	retry := 1
	if err := applyBoundedIntOverride(&retry, intPtr(4), "retry_count", 1, 10); err != nil {
		t.Fatalf("applyBoundedIntOverride() error = %v", err)
	}
	if retry != 4 {
		t.Fatalf("unexpected retry value %d", retry)
	}
	if err := applyBoundedIntOverride(&retry, intPtr(20), "retry_count", 1, 10); err == nil {
		t.Fatal("expected out-of-range error")
	}

	value := "old"
	applyStringOverride(&value, strPtr("new"))
	if value != "new" {
		t.Fatalf("unexpected string override %q", value)
	}
}

func TestTransportRankAndValidation(t *testing.T) {
	settings := DefaultSettings()
	rank := TransportRank(settings)
	if rank[model.TransportUSBDirect] != 0 || rank[model.TransportReceiver] != 1 {
		t.Fatalf("unexpected rank map %#v", rank)
	}

	if err := ValidateCoreSettings(settings); err != nil {
		t.Fatalf("ValidateCoreSettings(DefaultSettings()) error = %v", err)
	}
	if err := ValidateDBusSettings(settings); err != nil {
		t.Fatalf("ValidateDBusSettings(DefaultSettings()) error = %v", err)
	}

	settings.StaleTTLSec = settings.CacheTTLSec - 1
	if err := ValidateCoreSettings(settings); err == nil {
		t.Fatal("expected stale/cache TTL validation error")
	}

	settings = DefaultSettings()
	settings.DBusBusName = "invalid"
	if err := ValidateDBusSettings(settings); err == nil {
		t.Fatal("expected invalid bus name error")
	}

	settings = DefaultSettings()
	settings.DBusObjectPath = "invalid"
	if err := ValidateDBusSettings(settings); err == nil {
		t.Fatal("expected invalid object path error")
	}

	settings = DefaultSettings()
	settings.DBusInterfaceName = "invalid"
	if err := ValidateDBusSettings(settings); err == nil {
		t.Fatal("expected invalid interface name error")
	}

	settings = DefaultSettings()
	badForce := filepath.Join(t.TempDir(), "hidraw-missing")
	settings.ForceHidraw = &badForce
	if err := ValidateCoreSettings(settings); err == nil {
		t.Fatal("expected invalid force_hidraw error via ValidateCoreSettings")
	}
}

func TestValidateForceHidraw(t *testing.T) {
	if err := validateForceHidraw(""); err == nil {
		t.Fatal("expected empty force_hidraw error")
	}
	if err := validateForceHidraw("/dev/not-hidraw"); err == nil {
		t.Fatal("expected basename validation error")
	}

	dir := t.TempDir()
	regularPath := filepath.Join(dir, "hidraw0")
	if err := os.WriteFile(regularPath, []byte("not a char device"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := validateForceHidraw(regularPath); err == nil || !strings.Contains(err.Error(), "character device") {
		t.Fatalf("expected character device error, got %v", err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	symlinkPath := filepath.Join(dir, "hidraw1")
	if err := os.Symlink(target, symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := validateForceHidraw(symlinkPath); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestDefaultUserConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	if got := DefaultUserConfigPath(); got != filepath.Join("/tmp/xdg-config", "hi-drawbridge", "config.yaml") {
		t.Fatalf("unexpected XDG config path %q", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/home")
	if got := DefaultUserConfigPath(); got != filepath.Join("/tmp/home", ".config", "hi-drawbridge", "config.yaml") {
		t.Fatalf("unexpected HOME config path %q", got)
	}
}

func TestResolveSettingsInvalidOverrideSources(t *testing.T) {
	if _, err := ResolveSettings(&Overrides{RetryCount: intPtr(0)}, nil); err == nil || !strings.Contains(err.Error(), "invalid file overrides") {
		t.Fatalf("expected invalid file override error, got %v", err)
	}

	t.Setenv("HI_DRAWBRIDGE_log_level", "bogus")
	if _, err := ResolveSettings(nil, nil); err == nil || !strings.Contains(err.Error(), "invalid env overrides") {
		t.Fatalf("expected invalid env override error, got %v", err)
	}
	t.Setenv("HI_DRAWBRIDGE_log_level", "")

	if _, err := ResolveSettings(nil, &Overrides{LogLevel: strPtr("bogus")}); err == nil || !strings.Contains(err.Error(), "invalid cli overrides") {
		t.Fatalf("expected invalid cli override error, got %v", err)
	}
}

func TestNameValidationHelpers(t *testing.T) {
	if !isValidLogLevel("INFO") || isValidLogLevel("TRACE") {
		t.Fatal("unexpected log level validation result")
	}
	if !isValidBusName("org.example-app.Service") {
		t.Fatal("expected valid bus name")
	}
	if isValidBusName(":org.example.Service") {
		t.Fatal("expected colon-prefixed bus name to be invalid")
	}
	if !isValidInterfaceName("org.example.Service") {
		t.Fatal("expected valid interface name")
	}
	if isValidInterfaceName("org.example.") {
		t.Fatal("expected trailing-dot interface name to be invalid")
	}
	if !isBusNameChar('-') || isInterfaceNameChar('-') {
		t.Fatal("unexpected name character validation result")
	}
}

func strPtr(v string) *string {
	return &v
}
