//go:build linux

package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newProbeDebugCmdWithDeps(deps cliDeps) *cobra.Command {
	deps = resolveCLIDeps(deps)

	var compact bool

	cmd := &cobra.Command{
		Use:   "probe-debug",
		Short: "Detailed probe diagnostics",
		Long:  "Run a probe cycle with rich diagnostics including selected profiles, candidates, and per-profile probe results.",
		Args:  cobra.NoArgs,
		Example: "  hi-drawbridge probe-debug\n" +
			"  hi-drawbridge --profile keychron_m7 probe-debug --compact",
		PreRunE: runtimePreRunE(runtimeBuildOptions{validateDBusSettings: false}, deps),
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := getRuntime(cmd)
			if runtime == nil {
				return errors.New("runtime context not initialized")
			}

			result := runtime.Bridge.DebugProbe(cmd.Context())
			payload := buildProbeDebugOutput(runtime, result)

			output, err := jsonDump(payload, compact)
			if err != nil {
				return fmt.Errorf("encode probe-debug output: %w", err)
			}

			if _, err := fmt.Fprintln(cmd.OutOrStdout(), output); err != nil {
				return fmt.Errorf("write probe-debug output: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&compact, "compact", false, "Compact JSON output")

	return cmd
}
