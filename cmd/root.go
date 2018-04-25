package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// installCmd represents the install command
var RootCmd = &cobra.Command{
	Use:   "metahelm",
	Short: "Manage graphs of Helm charts",
}

func clierr(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, msg+"\n", args...)
	os.Exit(1)
}
