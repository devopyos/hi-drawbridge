//go:build linux

package main

import (
	"fmt"
	"os"

	"github.com/devopyos/hi-drawbridge/internal/cli"
)

var version = "0.1.0-dev"

func main() {
	rootCmd := cli.NewRootCmd(version)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
