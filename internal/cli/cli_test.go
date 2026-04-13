//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

type fakeBridge struct {
	probeFunc func(context.Context) ([]model.BatteryDevice, error)
	debugFunc func(context.Context) model.MultiBridgeRunResult
}

func (f *fakeBridge) ProbeDevices(ctx context.Context) ([]model.BatteryDevice, error) {
	if f.probeFunc != nil {
		return f.probeFunc(ctx)
	}

	return nil, nil
}

func (f *fakeBridge) DebugProbe(ctx context.Context) model.MultiBridgeRunResult {
	if f.debugFunc != nil {
		return f.debugFunc(ctx)
	}

	return model.MultiBridgeRunResult{}
}

type fakePollingSnapshot struct {
	runFunc   func(context.Context) error
	latest    []model.BatteryDevice
	refreshCh chan struct{}
}

func (f *fakePollingSnapshot) Run(ctx context.Context) error {
	if f.runFunc != nil {
		return f.runFunc(ctx)
	}

	return nil
}

func (f *fakePollingSnapshot) GetLatestDevices() []model.BatteryDevice {
	out := make([]model.BatteryDevice, len(f.latest))
	copy(out, f.latest)
	return out
}

func (f *fakePollingSnapshot) RefreshEvents() <-chan struct{} {
	return f.refreshCh
}

type fakeService struct {
	runFunc func(context.Context) error
}

func (f *fakeService) Run(ctx context.Context) error {
	if f.runFunc != nil {
		return f.runFunc(ctx)
	}

	return nil
}

func TestNewRootCmdSetsVersion(t *testing.T) {
	cmd := NewRootCmd("1.2.3-test")
	if cmd.Version != "1.2.3-test" {
		t.Fatalf("Version = %q, want %q", cmd.Version, "1.2.3-test")
	}
}

func TestCommandsRejectExtraArgs(t *testing.T) {
	runtime := newTestRuntime(t, &fakeBridge{})
	commands := []string{"probe", "probe-debug", "config", "serve"}

	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			stdout, _, err := executeRoot(t, context.Background(), runtime, command, "extra")
			if err == nil {
				t.Fatal("expected extra-args error")
			}
			if !strings.Contains(err.Error(), "accepts 0 arg(s), received 1") &&
				!strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("unexpected error %q", err)
			}
			if stdout != "" {
				t.Fatalf("expected no stdout, got %q", stdout)
			}
		})
	}
}

func TestProbeRequireDataReturnsErrorWithoutOutput(t *testing.T) {
	runtime := newTestRuntime(t, &fakeBridge{
		probeFunc: func(context.Context) ([]model.BatteryDevice, error) {
			return nil, nil
		},
	})

	stdout, _, err := executeRoot(t, context.Background(), runtime, "probe", "--require-data")
	if err == nil {
		t.Fatal("expected require-data error")
	}
	if !strings.Contains(err.Error(), "no devices found") {
		t.Fatalf("unexpected error %q", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout on require-data failure, got %q", stdout)
	}
}

func TestConfigOutputsResolvedRuntimeJSON(t *testing.T) {
	runtime := newTestRuntime(t, &fakeBridge{})
	runtime.Settings.LogLevel = "DEBUG"

	stdout, _, err := executeRoot(t, context.Background(), runtime, "config")
	if err != nil {
		t.Fatalf("Execute(config) error = %v", err)
	}

	var payload struct {
		ConfigExists        bool                   `json:"config_exists"`
		ConfigPath          string                 `json:"config_path"`
		ProfilesDir         string                 `json:"profiles_dir"`
		ProfilesDirExists   bool                   `json:"profiles_dir_exists"`
		ProfilesDirExplicit bool                   `json:"profiles_dir_explicit"`
		Profiles            []profile.ProfileDebug `json:"profiles"`
		SelectedProfileIDs  []string               `json:"selected_profile_ids"`
		Settings            config.Settings        `json:"settings"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal config output: %v", err)
	}

	if !payload.ConfigExists {
		t.Fatal("expected config_exists=true")
	}
	if payload.ConfigPath != "/tmp/hi-drawbridge.yaml" {
		t.Fatalf("unexpected config_path %q", payload.ConfigPath)
	}
	if payload.ProfilesDir != "/tmp/profiles" {
		t.Fatalf("unexpected profiles_dir %q", payload.ProfilesDir)
	}
	if !payload.ProfilesDirExists {
		t.Fatal("expected profiles_dir_exists=true")
	}
	if payload.ProfilesDirExplicit {
		t.Fatal("expected profiles_dir_explicit=false")
	}
	if len(payload.SelectedProfileIDs) != 1 || payload.SelectedProfileIDs[0] != "m7" {
		t.Fatalf("unexpected selected_profile_ids %#v", payload.SelectedProfileIDs)
	}
	if payload.Settings.LogLevel != "DEBUG" {
		t.Fatalf("unexpected settings.log_level %q", payload.Settings.LogLevel)
	}
	if len(payload.Profiles) != 1 || payload.Profiles[0].ID != "m7" {
		t.Fatalf("unexpected profiles payload %#v", payload.Profiles)
	}
}

func TestProbeDebugIncludesProfileMetadata(t *testing.T) {
	runtime := newTestRuntime(t, &fakeBridge{
		debugFunc: func(context.Context) model.MultiBridgeRunResult {
			return model.MultiBridgeRunResult{
				Devices: []model.BatteryDevice{
					{
						ID:         "mouse-1",
						Name:       "Mouse",
						DeviceType: "mouse",
						IconName:   "input-mouse",
						Percentage: 42,
						IsCharging: true,
					},
				},
				ProfileResults: []model.BridgeRunResult{
					{
						ProfileID: "m7",
						Source:    "probe",
						Devices: []model.BatteryDevice{
							{
								ID:         "mouse-1",
								Name:       "Mouse",
								DeviceType: "mouse",
								IconName:   "input-mouse",
								Percentage: 42,
								IsCharging: true,
							},
						},
					},
				},
			}
		},
	})

	stdout, _, err := executeRoot(t, context.Background(), runtime, "probe-debug", "--compact")
	if err != nil {
		t.Fatalf("Execute(probe-debug) error = %v", err)
	}

	var payload probeDebugOutput
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal probe-debug output: %v", err)
	}

	if len(payload.Devices) != 1 || payload.Devices[0].ID != "mouse-1" {
		t.Fatalf("unexpected devices payload %#v", payload.Devices)
	}
	if payload.ProfilesDir != "/tmp/profiles" {
		t.Fatalf("unexpected profiles_dir %q", payload.ProfilesDir)
	}
	if !payload.ProfilesDirExists {
		t.Fatal("expected profiles_dir_exists=true")
	}
	if payload.ProfilesDirExplicit {
		t.Fatal("expected profiles_dir_explicit=false")
	}
	if len(payload.SelectedProfileIDs) != 1 || payload.SelectedProfileIDs[0] != "m7" {
		t.Fatalf("unexpected selected_profile_ids %#v", payload.SelectedProfileIDs)
	}
	if len(payload.ProfileResults) != 1 {
		t.Fatalf("unexpected profile_results %#v", payload.ProfileResults)
	}
	if payload.ProfileResults[0].Profile == nil {
		t.Fatalf("expected embedded profile metadata, got %#v", payload.ProfileResults[0])
	}
	if payload.ProfileResults[0].Profile.ID != "m7" {
		t.Fatalf("unexpected embedded profile id %#v", payload.ProfileResults[0].Profile.ID)
	}
}

func TestServeDryRunExitsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runtime := newTestRuntime(t, &fakeBridge{
		probeFunc: func(context.Context) ([]model.BatteryDevice, error) {
			return nil, nil
		},
	})

	snapshotCreated := false
	serviceCreated := false

	stdout, _, err := executeRootWithDeps(t, ctx, runtime, cliDeps{
		newPollingSnapshot: func(
			fetchDevices func(context.Context) ([]model.BatteryDevice, error),
			intervalSec int,
			logger *slog.Logger,
		) (pollingSnapshot, error) {
			snapshotCreated = true
			return &fakePollingSnapshot{
				runFunc: func(ctx context.Context) error {
					<-ctx.Done()
					return nil
				},
				refreshCh: make(chan struct{}),
			}, nil
		},
		newDBusService: func(
			busName string,
			objectPath string,
			interfaceName string,
			fetchDevices func(context.Context) ([]model.BatteryDevice, error),
			logger *slog.Logger,
		) (serviceRunner, error) {
			serviceCreated = true
			return &fakeService{}, nil
		},
	}, "serve", "--dry-run")
	if err != nil {
		t.Fatalf("Execute(serve --dry-run) error = %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout, got %q", stdout)
	}
	if !snapshotCreated {
		t.Fatal("expected dry-run serve to create polling snapshot")
	}
	if serviceCreated {
		t.Fatal("expected dry-run serve to skip D-Bus service creation")
	}
}

func TestBuildRuntimeUsesEmbeddedProfilesAndCLIOverrides(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")

	profileID := "keychron_m7"
	logLevel := "debug"

	runtime, err := buildRuntime(nil, &profileID, nil, &logLevel, nil, runtimeBuildOptions{validateDBusSettings: true})
	if err != nil {
		t.Fatalf("buildRuntime() error = %v", err)
	}

	if runtime.UserConfig.Exists {
		t.Fatal("expected missing config file in temp HOME")
	}
	if len(runtime.SelectedProfiles) != 1 || runtime.SelectedProfiles[0].ID != "keychron_m7" {
		t.Fatalf("unexpected selected profiles %#v", runtime.SelectedProfiles)
	}
	if runtime.Settings.LogLevel != "DEBUG" {
		t.Fatalf("unexpected log level %q", runtime.Settings.LogLevel)
	}
	if runtime.ProfilesDir != filepath.Join(homeDir, ".config", "hi-drawbridge", "profiles") {
		t.Fatalf("unexpected profiles dir %q", runtime.ProfilesDir)
	}
	if runtime.ProfilesDirExists {
		t.Fatal("expected missing default profiles dir to report exists=false")
	}
	if runtime.ProfilesDirExplicit {
		t.Fatal("expected default profiles dir to report explicit=false")
	}
	if runtime.Bridge == nil {
		t.Fatal("expected bridge to be initialized")
	}
}

func TestBuildRuntimeRejectsUnknownProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	profileID := "missing"
	_, err := buildRuntime(nil, &profileID, nil, nil, nil, runtimeBuildOptions{})
	if err == nil {
		t.Fatal("expected unknown profile error")
	}
	if !strings.Contains(err.Error(), "invalid --profile value") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestBuildRuntimeRejectsInvalidCLIOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	logLevel := "bogus"
	_, err := buildRuntime(nil, nil, nil, &logLevel, nil, runtimeBuildOptions{})
	if err == nil {
		t.Fatal("expected invalid cli override error")
	}
	if !strings.Contains(err.Error(), "failed to resolve settings") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestResolveProfilesDirCLIBeatsEnv(t *testing.T) {
	t.Setenv(profilesDirEnvVar, "/tmp/from-env")

	resolved := resolveProfilesDir(strPtr(" /tmp/from-cli "))
	if resolved.Path != "/tmp/from-cli" {
		t.Fatalf("unexpected resolved path %q", resolved.Path)
	}
	if !resolved.Explicit {
		t.Fatal("expected cli-provided profiles dir to be explicit")
	}
}

func TestResolveProfilesDirUsesEnvOverride(t *testing.T) {
	t.Setenv(profilesDirEnvVar, " /tmp/from-env ")

	resolved := resolveProfilesDir(nil)
	if resolved.Path != "/tmp/from-env" {
		t.Fatalf("unexpected resolved path %q", resolved.Path)
	}
	if !resolved.Explicit {
		t.Fatal("expected env-provided profiles dir to be explicit")
	}
}

func TestBuildRuntimeLoadsLocalOverlayProfilesFromDefaultDir(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")

	overlayDir := filepath.Join(homeDir, ".config", "hi-drawbridge", "profiles")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	localProfilePath := filepath.Join(overlayDir, "local-test.yaml")
	if err := os.WriteFile(localProfilePath, []byte(testProfileYAML("local-test", "Local Test Device", "1000", "2000")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	profileID := "local-test"
	runtime, err := buildRuntime(nil, &profileID, nil, nil, nil, runtimeBuildOptions{})
	if err != nil {
		t.Fatalf("buildRuntime() error = %v", err)
	}

	if runtime.ProfilesDir != overlayDir {
		t.Fatalf("unexpected profiles dir %q", runtime.ProfilesDir)
	}
	if !runtime.ProfilesDirExists {
		t.Fatal("expected local profiles dir to exist")
	}
	if runtime.ProfilesDirExplicit {
		t.Fatal("expected default local profiles dir to be implicit")
	}
	if len(runtime.SelectedProfiles) != 1 || runtime.SelectedProfiles[0].ID != "local-test" {
		t.Fatalf("unexpected selected profiles %#v", runtime.SelectedProfiles)
	}
	if runtime.SelectedProfiles[0].SourcePath != localProfilePath {
		t.Fatalf("unexpected local profile source path %q", runtime.SelectedProfiles[0].SourcePath)
	}
}

func TestBuildRuntimeLocalProfilesOverrideEmbeddedProfiles(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")

	overlayDir := filepath.Join(homeDir, ".config", "hi-drawbridge", "profiles")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	overridePath := filepath.Join(overlayDir, "m7.yaml")
	if err := os.WriteFile(overridePath, []byte(testProfileYAML("keychron_m7", "Local M7 Override", "d044", "d030")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	profileID := "keychron_m7"
	runtime, err := buildRuntime(nil, &profileID, nil, nil, nil, runtimeBuildOptions{})
	if err != nil {
		t.Fatalf("buildRuntime() error = %v", err)
	}

	if runtime.SelectedProfiles[0].Name != "Local M7 Override" {
		t.Fatalf("expected local override to win, got %q", runtime.SelectedProfiles[0].Name)
	}
	if runtime.SelectedProfiles[0].SourcePath != overridePath {
		t.Fatalf("unexpected override source path %q", runtime.SelectedProfiles[0].SourcePath)
	}
}

func TestBuildRuntimeRejectsMissingExplicitProfilesDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	profilesDir := filepath.Join(t.TempDir(), "missing-profiles")
	_, err := buildRuntime(nil, nil, nil, nil, &profilesDir, runtimeBuildOptions{})
	if err == nil {
		t.Fatal("expected missing explicit profiles dir error")
	}
	if !strings.Contains(err.Error(), "failed to load profiles") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestBuildRuntimeProfilesDirCLIBeatsEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(profilesDirEnvVar, filepath.Join(t.TempDir(), "env-profiles"))

	cliProfilesDir := filepath.Join(t.TempDir(), "cli-profiles")
	if err := os.MkdirAll(cliProfilesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	localProfilePath := filepath.Join(cliProfilesDir, "local-test.yaml")
	if err := os.WriteFile(localProfilePath, []byte(testProfileYAML("local-test", "CLI Local Test", "1000", "2000")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	profileID := "local-test"
	runtime, err := buildRuntime(nil, &profileID, nil, nil, &cliProfilesDir, runtimeBuildOptions{})
	if err != nil {
		t.Fatalf("buildRuntime() error = %v", err)
	}

	if runtime.ProfilesDir != cliProfilesDir {
		t.Fatalf("expected cli profiles dir to win, got %q", runtime.ProfilesDir)
	}
	if !runtime.ProfilesDirExplicit {
		t.Fatal("expected cli profiles dir to be explicit")
	}
	if runtime.SelectedProfiles[0].SourcePath != localProfilePath {
		t.Fatalf("unexpected selected profile source path %q", runtime.SelectedProfiles[0].SourcePath)
	}
}

func TestGetRuntimeAndNonEmptyStringValue(t *testing.T) {
	cmd := newConfigCmdWithDeps(cliDeps{})
	if got := getRuntime(cmd); got != nil {
		t.Fatalf("expected nil runtime, got %#v", got)
	}

	cmd.SetContext(context.WithValue(context.Background(), contextKey{}, "wrong-type"))
	if got := getRuntime(cmd); got != nil {
		t.Fatalf("expected nil runtime for wrong type, got %#v", got)
	}

	runtime := newTestRuntime(t, &fakeBridge{})
	cmd.SetContext(context.WithValue(context.Background(), contextKey{}, runtime))
	if got := getRuntime(cmd); got != runtime {
		t.Fatalf("expected stored runtime, got %#v", got)
	}

	if got := nonEmptyStringValue(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %#v", got)
	}
	if got := nonEmptyStringValue(strPtr("   ")); got != nil {
		t.Fatalf("expected nil for blank input, got %#v", got)
	}
	if got := nonEmptyStringValue(strPtr("  m7 ")); got == nil || *got != "m7" {
		t.Fatalf("unexpected trimmed value %#v", got)
	}
}

func TestRunServeNonDryRunUsesPollingAndDBus(t *testing.T) {
	runtime := newTestRuntime(t, &fakeBridge{})

	snapshotCreated := false
	serviceCreated := false
	if err := runServeWithDeps(context.Background(), runtime, false, cliDeps{
		newPollingSnapshot: func(
			fetchDevices func(context.Context) ([]model.BatteryDevice, error),
			intervalSec int,
			logger *slog.Logger,
		) (pollingSnapshot, error) {
			snapshotCreated = true
			return &fakePollingSnapshot{
				runFunc:   func(context.Context) error { return nil },
				refreshCh: make(chan struct{}),
			}, nil
		},
		newDBusService: func(
			busName string,
			objectPath string,
			interfaceName string,
			fetchDevices func(context.Context) ([]model.BatteryDevice, error),
			logger *slog.Logger,
		) (serviceRunner, error) {
			serviceCreated = true
			return &fakeService{}, nil
		},
	}); err != nil {
		t.Fatalf("runServe() error = %v", err)
	}
	if !snapshotCreated {
		t.Fatal("expected polling snapshot to be created")
	}
	if !serviceCreated {
		t.Fatal("expected D-Bus service to be created")
	}
}

func TestRunServeReturnsSnapshotInitializationError(t *testing.T) {
	runtime := newTestRuntime(t, &fakeBridge{})

	err := runServeWithDeps(context.Background(), runtime, false, cliDeps{
		newPollingSnapshot: func(
			fetchDevices func(context.Context) ([]model.BatteryDevice, error),
			intervalSec int,
			logger *slog.Logger,
		) (pollingSnapshot, error) {
			return nil, errors.New("snapshot init failed")
		},
	})
	if err == nil {
		t.Fatal("expected runServe() error")
	}
	if !strings.Contains(err.Error(), "initialize polling snapshot") {
		t.Fatalf("unexpected error %q", err)
	}
}

func executeRoot(t *testing.T, ctx context.Context, runtime *RuntimeContext, args ...string) (string, string, error) {
	t.Helper()

	return executeRootWithDeps(t, ctx, runtime, cliDeps{}, args...)
}

func executeRootWithDeps(t *testing.T, ctx context.Context, runtime *RuntimeContext, deps cliDeps, args ...string) (string, string, error) {
	t.Helper()

	deps.buildRuntime = func(
		configPath, profileID, forceHidraw, logLevel, profilesDir *string,
		options runtimeBuildOptions,
	) (*RuntimeContext, error) {
		return runtime, nil
	}

	cmd := newRootCmd("1.2.3-test", deps)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	if ctx != nil {
		cmd.SetContext(ctx)
	}

	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func newTestRuntime(t *testing.T, bridge bridgeRuntime) *RuntimeContext {
	t.Helper()

	registry, err := profile.BuildProfileRegistry([]profile.ProfileSpec{
		{
			ID:         "m7",
			Name:       "Keychron M7",
			SourcePath: "profiles/keychron_m7.yaml",
			DeviceType: "mouse",
			IconName:   "input-mouse",
		},
	})
	if err != nil {
		t.Fatalf("BuildProfileRegistry() error = %v", err)
	}

	selectedProfiles, err := registry.SelectProfiles(strPtr("m7"))
	if err != nil {
		t.Fatalf("SelectProfiles() error = %v", err)
	}

	return &RuntimeContext{
		Settings:            config.DefaultSettings(),
		UserConfig:          &config.LoadedUserConfig{Path: "/tmp/hi-drawbridge.yaml", Exists: true, BridgeOverrides: &config.Overrides{}},
		ProfilesDir:         "/tmp/profiles",
		ProfilesDirExists:   true,
		ProfilesDirExplicit: false,
		ProfileRegistry:     registry,
		SelectedProfiles:    selectedProfiles,
		Logger:              slog.New(slog.DiscardHandler),
		Bridge:              bridge,
	}
}

func strPtr(v string) *string {
	return &v
}

func testProfileYAML(id, name, usbProductID, receiverProductID string) string {
	return "" +
		"id: " + id + "\n" +
		"name: " + name + "\n" +
		"vendor_id: 3434\n" +
		"transport_product_ids:\n" +
		"  usb_direct: " + usbProductID + "\n" +
		"  receiver: " + receiverProductID + "\n" +
		"device_type: mouse\n" +
		"icon_name: input-mouse\n" +
		"probe_path: feature_or_interrupt\n" +
		"wake_report_hex: 00b200000000000000000000000000000000000000000000000000000000000000\n" +
		"query_report_id: 81\n" +
		"prime_query_cmd: 7\n" +
		"battery_query_cmd: 6\n" +
		"query_length: 21\n" +
		"expected_signature_hex: 343444d0\n" +
		"battery_offset: 11\n" +
		"status_offset: 12\n" +
		"fallback_input_report_id: 84\n" +
		"fallback_input_cmd: 228\n" +
		"fallback_input_length: 21\n" +
		"fallback_battery_offset: 2\n" +
		"fallback_battery_bucket_max: 4\n" +
		"charging_status_bytes: [1]\n" +
		"query_endpoint:\n" +
		"  required_feature_report_ids: [81]\n" +
		"wake_endpoint:\n" +
		"  interface_numbers: [3]\n"
}
