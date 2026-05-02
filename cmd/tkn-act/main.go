package main

import (
	"fmt"
	"os"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitcode.From(err))
	}
}
