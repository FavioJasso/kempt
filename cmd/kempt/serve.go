package main

import (
	"errors"

	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web demo and JSON API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("serve is not implemented yet")
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default :$PORT, else :8080)")
	return cmd
}
