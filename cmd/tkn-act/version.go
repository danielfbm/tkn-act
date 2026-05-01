package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "dev" // overridden via -ldflags

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("tkn-act", version)
			return nil
		},
	}
}
