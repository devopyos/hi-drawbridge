//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/devopyos/hi-drawbridge/internal/dbus"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/polling"
)

type pollingSnapshot interface {
	Run(context.Context) error
	GetLatestDevices() []model.BatteryDevice
	RefreshEvents() <-chan struct{}
}

type serviceRunner interface {
	Run(context.Context) error
}

type pollingSnapshotFactory func(
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	intervalSec int,
	logger *slog.Logger,
) (pollingSnapshot, error)

type dbusServiceFactory func(
	busName string,
	objectPath string,
	interfaceName string,
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	logger *slog.Logger,
) (serviceRunner, error)

func defaultPollingSnapshotFactory(
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	intervalSec int,
	logger *slog.Logger,
) (pollingSnapshot, error) {
	return polling.NewPollingDeviceSnapshot(fetchDevices, intervalSec, logger)
}

func defaultDBusServiceFactory(
	busName string,
	objectPath string,
	interfaceName string,
	fetchDevices func(context.Context) ([]model.BatteryDevice, error),
	logger *slog.Logger,
) (serviceRunner, error) {
	return dbus.NewService(busName, objectPath, interfaceName, fetchDevices, logger)
}

func newServeCmdWithDeps(deps cliDeps) *cobra.Command {
	deps = resolveCLIDeps(deps)
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the D-Bus service",
		Long:  "Run the bridge poller and publish BatteryWatch companion data over the session D-Bus bus.",
		Args:  cobra.NoArgs,
		Example: "  hi-drawbridge serve\n" +
			"  hi-drawbridge --profile keychron_m7 serve\n" +
			"  hi-drawbridge serve --dry-run",
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return attachRuntime(cmd, runtimeBuildOptions{validateDBusSettings: !dryRun}, deps)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt := getRuntime(cmd)
			if rt == nil {
				return errors.New("runtime context not initialized")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return runServeWithDeps(ctx, rt, dryRun, deps)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Poll and log devices without opening D-Bus")

	return cmd
}

func runServeWithDeps(ctx context.Context, rt *RuntimeContext, dryRun bool, deps cliDeps) error {
	deps = resolveCLIDeps(deps)

	snapshot, err := deps.newPollingSnapshot(
		rt.Bridge.ProbeDevices,
		rt.Settings.ServicePollIntervalSec,
		rt.Logger,
	)
	if err != nil {
		return fmt.Errorf("initialize polling snapshot: %w", err)
	}

	if dryRun {
		rt.Logger.Info("hi-drawbridge serving (dry-run)",
			"poll_interval_sec", rt.Settings.ServicePollIntervalSec,
		)

		return runDryRun(ctx, rt, snapshot)
	}

	service, err := deps.newDBusService(
		rt.Settings.DBusBusName,
		rt.Settings.DBusObjectPath,
		rt.Settings.DBusInterfaceName,
		func(context.Context) ([]model.BatteryDevice, error) {
			return snapshot.GetLatestDevices(), nil
		},
		rt.Logger,
	)
	if err != nil {
		return fmt.Errorf("initialize dbus service: %w", err)
	}

	rt.Logger.Info("hi-drawbridge serving",
		"poll_interval_sec", rt.Settings.ServicePollIntervalSec,
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return snapshot.Run(gctx)
	})
	g.Go(func() error {
		return service.Run(gctx)
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("serve failed: %w", err)
	}

	return nil
}

func runDryRun(ctx context.Context, rt *RuntimeContext, snapshot pollingSnapshot) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return snapshot.Run(gctx)
	})
	g.Go(func() error {
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-snapshot.RefreshEvents():
				devices := snapshot.GetLatestDevices()
				for _, d := range devices {
					rt.Logger.Info("dry-run device",
						"id", d.ID,
						"percentage", d.Percentage,
						"charging", d.IsCharging,
					)
				}
			}
		}
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("dry-run serve failed: %w", err)
	}

	return nil
}
