//go:build linux

package config

import (
	"strings"
	"testing"
)

func TestLoadEnvOverridesNewPrefix(t *testing.T) {
	t.Setenv("HI_DRAWBRIDGE_log_level", "INFO")

	o, err := loadEnvOverrides()
	if err != nil {
		t.Fatalf("loadEnvOverrides() error = %v", err)
	}

	if o.LogLevel == nil {
		t.Fatalf("expected new log_level override to be set")
	}

	if *o.LogLevel != "INFO" {
		t.Fatalf("expected new log_level override %q, got %q", "INFO", *o.LogLevel)
	}
}

func TestLoadEnvOverridesBlankValuesIgnored(t *testing.T) {
	t.Setenv("HI_DRAWBRIDGE_log_level", "")
	t.Setenv("HI_DRAWBRIDGE_retry_count", "")

	o, err := loadEnvOverrides()
	if err != nil {
		t.Fatalf("loadEnvOverrides() error = %v", err)
	}

	if o.LogLevel != nil {
		t.Fatalf("expected blank log_level override to be ignored")
	}

	if o.RetryCount != nil {
		t.Fatalf("expected blank retry_count override to be ignored")
	}
}

func TestLoadEnvOverridesInvalidInt(t *testing.T) {
	t.Setenv("HI_DRAWBRIDGE_retry_count", "nope")

	_, err := loadEnvOverrides()
	if err == nil {
		t.Fatalf("expected error for invalid integer override")
	}

	if !strings.Contains(err.Error(), "HI_DRAWBRIDGE_retry_count") {
		t.Fatalf("expected error to reference new prefix, got %q", err.Error())
	}
}

func TestResolveSettingsLayering(t *testing.T) {
	fileOverrides := &Overrides{
		RetryCount: intPtr(4),
	}
	cliOverrides := &Overrides{
		RetryCount: intPtr(6),
	}
	t.Setenv("HI_DRAWBRIDGE_retry_count", "5")

	s, err := ResolveSettings(fileOverrides, cliOverrides)
	if err != nil {
		t.Fatalf("ResolveSettings() error = %v", err)
	}

	if s.RetryCount != 6 {
		t.Fatalf("expected cli override to win, got retry_count=%d", s.RetryCount)
	}
}

func intPtr(v int) *int {
	return &v
}
