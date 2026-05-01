package main

import (
	"fmt"

	"github.com/dfbmorinigo/tkn-act/internal/discovery"
	"github.com/dfbmorinigo/tkn-act/internal/loader"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Pipelines and Tasks discovered in the project",
		RunE: func(c *cobra.Command, _ []string) error {
			if dir == "" {
				dir = "."
			}
			files, err := discovery.Find(dir)
			if err != nil {
				return err
			}
			b, err := loader.LoadFiles(files)
			if err != nil {
				return err
			}
			fmt.Println("Pipelines:")
			for n := range b.Pipelines {
				fmt.Printf("  - %s\n", n)
			}
			fmt.Println("Tasks:")
			for n := range b.Tasks {
				fmt.Printf("  - %s\n", n)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "directory to scan (default: cwd)")
	return cmd
}
