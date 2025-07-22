package main

import (
	"log"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "checktrace",
	Short: "Analyze and process trace events for tail-based sampling",
	Long: `Analyze and process trace events for tail-based sampling.
The command will also output a summary for each traceID.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		inputFilePath, _ := cmd.Flags().GetString("input-file")
		configFilePath, _ := cmd.Flags().GetString("config")
		return runTraceCheck(inputFilePath, configFilePath)
	},
}

func init() {
	rootCmd.Flags().StringP("input-file", "i", "", "File containing trace event documents to process")
	rootCmd.Flags().StringP("config", "c", "", "Configuration file (optional - uses default config if not provided)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
