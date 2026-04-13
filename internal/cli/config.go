//go:build linux

package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newConfigCmdWithDeps(deps cliDeps) *cobra.Command {
	deps = resolveCLIDeps(deps)

	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show resolved configuration",
		Long:  "Print resolved runtime settings, selected profiles, and config-source metadata as JSON.",
		Args:  cobra.NoArgs,
		Example: "  hi-drawbridge config\n" +
			"  hi-drawbridge --config /tmp/hi-drawbridge.yaml config",
		PreRunE: runtimePreRunE(runtimeBuildOptions{validateDBusSettings: false}, deps),
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := getRuntime(cmd)
			if runtime == nil {
				return errors.New("runtime context not initialized")
			}

			payload := buildConfigOutput(runtime)

			output, err := jsonDump(payload, false)
			if err != nil {
				return fmt.Errorf("encode config output: %w", err)
			}

			if _, err := fmt.Fprintln(cmd.OutOrStdout(), output); err != nil {
				return fmt.Errorf("write config output: %w", err)
			}

			return nil
		},
	}

	return cmd
}
