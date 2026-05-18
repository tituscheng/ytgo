package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of ytgo",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("ytgo", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
