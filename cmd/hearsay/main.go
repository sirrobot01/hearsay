// Command hearsay is the Hearsay CLI: the daemon and the Torznab
// proxy as subcommands of one binary.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:          "hearsay",
		Short:        "Share observations about external state, as byproducts of work already being done",
		Version:      version,
		SilenceUsage: true,
	}
	root.AddCommand(serveCmd(), torznabCmd(), statusCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
