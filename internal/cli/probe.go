//go:build linux

package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func jsonDump(v any, compact bool) (string, error) {
	if compact {
		data, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal compact json: %w", err)
		}

		return string(data), nil
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal indented json: %w", err)
	}

	return string(data), nil
}

func newProbeCmdWithDeps(deps cliDeps) *cobra.Command {
	deps = resolveCLIDeps(deps)

	var compact bool
	var requireData bool

	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Probe HID devices and output battery data",
		Long:  "Run one probe cycle and print discovered battery-capable devices as JSON.",
		Args:  cobra.NoArgs,
		Example: "  hi-drawbridge probe\n" +
			"  hi-drawbridge --profile keychron_m7 probe --compact\n" +
			"  hi-drawbridge probe --require-data",
		PreRunE: runtimePreRunE(runtimeBuildOptions{validateDBusSettings: false}, deps),
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := getRuntime(cmd)
			if runtime == nil {
				return errors.New("runtime context not initialized")
			}

			devices, err := runtime.Bridge.ProbeDevices(cmd.Context())
			if err != nil {
				return fmt.Errorf("probe failed: %w", err)
			}

			if requireData && len(devices) == 0 {
				return errors.New("no devices found")
			}

			output, err := jsonDump(devices, compact)
			if err != nil {
				return fmt.Errorf("encode probe output: %w", err)
			}

			if _, err := fmt.Fprintln(cmd.OutOrStdout(), output); err != nil {
				return fmt.Errorf("write probe output: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&compact, "compact", false, "Compact JSON output")
	cmd.Flags().BoolVar(&requireData, "require-data", false, "Exit with error if no devices found")

	return cmd
}
