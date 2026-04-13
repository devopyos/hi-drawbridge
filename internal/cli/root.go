//go:build linux

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devopyos/hi-drawbridge/internal/bridge"
	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

type contextKey struct{}

type bridgeRuntime interface {
	ProbeDevices(context.Context) ([]model.BatteryDevice, error)
	DebugProbe(context.Context) model.MultiBridgeRunResult
}

// RuntimeContext holds all resolved runtime dependencies shared across CLI subcommands.
type RuntimeContext struct {
	Settings            config.Settings
	UserConfig          *config.LoadedUserConfig
	ProfilesDir         string
	ProfilesDirExists   bool
	ProfilesDirExplicit bool
	ProfileRegistry     *profile.ProfileRegistry
	SelectedProfiles    []profile.ProfileSpec
	Logger              *slog.Logger
	Bridge              bridgeRuntime
}

type runtimeBuilder func(configPath, profileID, forceHidraw, logLevel, profilesDir *string, options runtimeBuildOptions) (*RuntimeContext, error)

type cliDeps struct {
	buildRuntime       runtimeBuilder
	newPollingSnapshot pollingSnapshotFactory
	newDBusService     dbusServiceFactory
}

// NewRootCmd creates the root cobra command with persistent flags and subcommand registration.
func NewRootCmd(version string) *cobra.Command {
	return newRootCmd(version, cliDeps{})
}

func newRootCmd(version string, deps cliDeps) *cobra.Command {
	deps = resolveCLIDeps(deps)

	rootCmd := &cobra.Command{
		Use:           "hi-drawbridge",
		Short:         "Linux HID device battery monitor with D-Bus integration",
		Long:          "hi-drawbridge bridges Linux hidraw battery readings to the BatteryWatch companion D-Bus API. Use explicit subcommands such as probe or serve.",
		Example:       "  hi-drawbridge probe\n  hi-drawbridge --profile keychron_m7 probe-debug\n  hi-drawbridge --profiles-dir ~/.config/hi-drawbridge/profiles --profile my-device probe-debug\n  hi-drawbridge serve\n  hi-drawbridge --config ~/.config/hi-drawbridge/config.yaml config",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	rootCmd.PersistentFlags().String("config", "", "Path to YAML config file")
	rootCmd.PersistentFlags().String("profile", "", "Profile ID to use")
	rootCmd.PersistentFlags().String("profiles-dir", "", "Directory with local profile YAML overlays")
	rootCmd.PersistentFlags().String("force-hidraw", "", "Force specific hidraw device path")
	rootCmd.PersistentFlags().String("log-level", "", "Log level (DEBUG, INFO, WARNING, ERROR)")

	rootCmd.AddCommand(newServeCmdWithDeps(deps))
	rootCmd.AddCommand(newProbeCmdWithDeps(deps))
	rootCmd.AddCommand(newProbeDebugCmdWithDeps(deps))
	rootCmd.AddCommand(newConfigCmdWithDeps(deps))

	return rootCmd
}

type runtimeBuildOptions struct {
	validateDBusSettings bool
}

func resolveCLIDeps(deps cliDeps) cliDeps {
	if deps.buildRuntime == nil {
		deps.buildRuntime = buildRuntime
	}
	if deps.newPollingSnapshot == nil {
		deps.newPollingSnapshot = defaultPollingSnapshotFactory
	}
	if deps.newDBusService == nil {
		deps.newDBusService = defaultDBusServiceFactory
	}

	return deps
}

func attachRuntime(cmd *cobra.Command, options runtimeBuildOptions, deps cliDeps) error {
	configPath, _ := cmd.Flags().GetString("config")
	profileID, _ := cmd.Flags().GetString("profile")
	profilesDir, _ := cmd.Flags().GetString("profiles-dir")
	forceHidraw, _ := cmd.Flags().GetString("force-hidraw")
	logLevel, _ := cmd.Flags().GetString("log-level")

	rt, err := deps.buildRuntime(&configPath, &profileID, &forceHidraw, &logLevel, &profilesDir, options)
	if err != nil {
		return err
	}

	cmd.SetContext(context.WithValue(cmd.Context(), contextKey{}, rt))
	return nil
}

func runtimePreRunE(options runtimeBuildOptions, deps cliDeps) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		return attachRuntime(cmd, options, deps)
	}
}

func buildRuntime(configPath, profileID, forceHidraw, logLevel, profilesDir *string, options runtimeBuildOptions) (*RuntimeContext, error) {
	userConfig, err := config.LoadUserConfig(nonEmptyStringValue(configPath))
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	cliOverrides := &config.Overrides{
		ForceHidraw: forceHidraw,
	}

	if logLevel != nil && *logLevel != "" {
		cliOverrides.LogLevel = logLevel
	}

	settings, err := config.ResolveSettings(userConfig.BridgeOverrides, cliOverrides)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve settings: %w", err)
	}

	if err := config.ValidateCoreSettings(settings); err != nil {
		return nil, fmt.Errorf("invalid resolved settings: %w", err)
	}

	if options.validateDBusSettings {
		if err := config.ValidateDBusSettings(settings); err != nil {
			return nil, fmt.Errorf("invalid resolved settings: %w", err)
		}
	}

	logger := setupLogger(settings.LogLevel)
	profilesDirConfig := resolveProfilesDir(profilesDir)

	registry, profilesDirExists, err := profile.LoadProfileRegistryWithOverlayDir(profilesDirConfig.Path, !profilesDirConfig.Explicit)
	if err != nil {
		return nil, fmt.Errorf("failed to load profiles: %w", err)
	}
	selectedProfiles, err := registry.SelectProfiles(nonEmptyStringValue(profileID))
	if err != nil {
		return nil, fmt.Errorf("invalid --profile value: %w", err)
	}

	multiBridge := bridge.NewMultiHidBatteryBridge(settings, selectedProfiles, logger, nil)

	return &RuntimeContext{
		Settings:            settings,
		UserConfig:          userConfig,
		ProfilesDir:         profilesDirConfig.Path,
		ProfilesDirExists:   profilesDirExists,
		ProfilesDirExplicit: profilesDirConfig.Explicit,
		ProfileRegistry:     registry,
		SelectedProfiles:    selectedProfiles,
		Logger:              logger,
		Bridge:              multiBridge,
	}, nil
}

func setupLogger(level string) *slog.Logger {
	var slogLevel slog.Level

	switch level {
	case "DEBUG":
		slogLevel = slog.LevelDebug
	case "WARNING":
		slogLevel = slog.LevelWarn
	case "ERROR":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel})
	return slog.New(handler)
}

func getRuntime(cmd *cobra.Command) *RuntimeContext {
	ctx := cmd.Context()
	if ctx == nil {
		return nil
	}

	val := ctx.Value(contextKey{})
	if val == nil {
		return nil
	}

	rt, ok := val.(*RuntimeContext)
	if !ok {
		return nil
	}

	return rt
}

func nonEmptyStringValue(value *string) *string {
	if value == nil {
		return nil
	}

	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}

	return &trimmed
}

const profilesDirEnvVar = "HI_DRAWBRIDGE_profiles_dir"

type profileDirConfig struct {
	Path     string
	Explicit bool
}

func resolveProfilesDir(cliValue *string) profileDirConfig {
	if value := nonEmptyStringValue(cliValue); value != nil {
		return profileDirConfig{Path: *value, Explicit: true}
	}

	if envValue := strings.TrimSpace(os.Getenv(profilesDirEnvVar)); envValue != "" {
		return profileDirConfig{Path: envValue, Explicit: true}
	}

	return profileDirConfig{
		Path:     defaultProfilesDirPath(),
		Explicit: false,
	}
}

func defaultProfilesDirPath() string {
	return filepath.Join(filepath.Dir(config.DefaultUserConfigPath()), "profiles")
}
