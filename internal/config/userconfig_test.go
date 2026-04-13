//go:build linux

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUserConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	cfg, err := LoadUserConfig(&path)
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}

	if cfg.Exists {
		t.Fatal("expected missing config to report Exists=false")
	}
	if cfg.Path != path {
		t.Fatalf("expected path %q, got %q", path, cfg.Path)
	}
	if cfg.BridgeOverrides == nil {
		t.Fatal("expected BridgeOverrides to be initialized")
	}
}

func TestLoadUserConfigBridgeOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bridge:\n  log_level: debug\n  retry_count: 9\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadUserConfig(&path)
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}

	if !cfg.Exists {
		t.Fatal("expected config to exist")
	}
	if cfg.BridgeOverrides == nil || cfg.BridgeOverrides.LogLevel == nil || cfg.BridgeOverrides.RetryCount == nil {
		t.Fatalf("expected bridge overrides to be loaded, got %#v", cfg.BridgeOverrides)
	}
	if *cfg.BridgeOverrides.LogLevel != "debug" {
		t.Fatalf("unexpected log level %q", *cfg.BridgeOverrides.LogLevel)
	}
	if *cfg.BridgeOverrides.RetryCount != 9 {
		t.Fatalf("unexpected retry count %d", *cfg.BridgeOverrides.RetryCount)
	}
}

func TestLoadUserConfigRejectsInlineProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n  - id: inline-profile\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadUserConfig(&path)
	if err == nil {
		t.Fatal("expected inline profiles to be rejected")
	}

	if !strings.Contains(err.Error(), "profiles are not supported") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestDecodeUserConfigYAMLRejectsMultipleDocuments(t *testing.T) {
	_, err := decodeUserConfigYAML("config.yaml", []byte("bridge:\n  log_level: INFO\n---\nbridge:\n  log_level: DEBUG\n"))
	if err == nil {
		t.Fatal("expected multiple-document YAML error")
	}

	if !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("unexpected error %q", err)
	}
}
