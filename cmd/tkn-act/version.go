package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev" // overridden via -ldflags

type versionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Example: `  tkn-act version
  tkn-act version -o json`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if gf.output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(versionInfo{Name: "tkn-act", Version: version})
			}
			fmt.Println("tkn-act", version)
			return nil
		},
	}
}
