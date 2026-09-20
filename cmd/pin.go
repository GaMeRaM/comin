package cmd

import (
	"fmt"

	"github.com/nlewo/comin/internal/fetcher"
	"github.com/nlewo/comin/internal/types"
	"github.com/spf13/cobra"
)

func init() {
	config := types.Niks3Fetcher{Timeout: 30}
	cmd := &cobra.Command{
		Use:   "pin URL",
		Short: "Read and validate a niks3 pin without a running agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config.URL = args[0]
			if config.Timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			path, err := fetcher.ReadNiks3Pin(cmd.Context(), config)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
	cmd.Flags().StringVar(&config.AWSCredentialsFile, "aws-credentials-file", "", "Runtime AWS credentials file")
	cmd.Flags().StringVar(&config.NetrcFile, "netrc-file", "", "Runtime netrc file for HTTPS Basic authentication")
	cmd.Flags().IntVar(&config.Timeout, "timeout", 30, "Pin request timeout in seconds")
	rootCmd.AddCommand(cmd)
}
