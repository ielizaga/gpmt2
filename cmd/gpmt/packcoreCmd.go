package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// PackcoreOptions defines a structure with the runtime options for the packcore command
type PackcoreOptions struct {
	core       string
	binary     string
	cleanup    bool
	ignoreLibs bool
}

// packcoreCmd is the cobra command definition for the packcore utility
var packcoreCmd = &cobra.Command{
	Use:   "packcore",
	Short: "core file collection",
	Long: `packcore takes a core file, extracts the name of the binary which
  generated the core, executes ldd (List Dynamic Dependencies) to get the required
  shared libraries and packages everything into a single tarball archive.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("I will be a core collector soon")
	},
}

// flagsPackcore assigns PackcoreOptions to the cobra packcoreCmd
func flagsPackcore() {
	packcoreCmd.Flags().StringVar(&packcoreOpts.core, "core", "", "Core file path")
	packcoreCmd.Flags().StringVar(&packcoreOpts.binary, "binary", "", "Binary path")
	packcoreCmd.Flags().BoolVar(&packcoreOpts.cleanup, "keep-tmp", false, "Do not clean up temp directory after packcore finishes (defaults to false)")
	packcoreCmd.Flags().BoolVar(&packcoreOpts.ignoreLibs, "ignore-libs", false, "Ignore missing libraries (defaults to false)")
}
