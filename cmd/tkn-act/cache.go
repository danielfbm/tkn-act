package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/spf13/cobra"
)

// Flags for the cache subcommands. Globals so tests can drive them.
var (
	cachePruneOlder time.Duration
	cacheYes        bool
)

// cacheListResult is the JSON shape printed by `tkn-act cache list -o json`.
// Stable contract: agents may depend on the field names.
type cacheListResult struct {
	Root    string                  `json:"root"`
	Entries []refresolver.CacheEntry `json:"entries"`
}

// cachePruneResult is the JSON shape printed by `tkn-act cache prune`.
type cachePruneResult struct {
	Root      string `json:"root"`
	OlderThan string `json:"older_than"`
	Pruned    int    `json:"pruned"`
}

// cacheClearResult is the JSON shape printed by `tkn-act cache clear`.
type cacheClearResult struct {
	Root    string `json:"root"`
	Cleared int    `json:"cleared"`
}

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and manage the resolver cache",
		Long: `Inspect and manage the on-disk resolver cache populated by every
direct/remote resolver dispatch. The cache root defaults to
$XDG_CACHE_HOME/tkn-act/resolved/ and can be overridden with
--resolver-cache-dir.

Subcommands: list, prune, clear.`,
		Example: `  # List every cached entry
  tkn-act cache list

  # Same, JSON shape (for agents)
  tkn-act cache list -o json

  # Delete entries older than 7 days
  tkn-act cache prune --older-than 168h

  # Wipe everything (-y required for non-interactive use)
  tkn-act cache clear -y`,
	}

	listCmd := &cobra.Command{
		Use:     "list",
		Short:   "List cached resolver entries",
		Example: `  tkn-act cache list -o json`,
		RunE:    func(_ *cobra.Command, _ []string) error { return runCacheList() },
	}
	pruneCmd := &cobra.Command{
		Use:     "prune",
		Short:   "Delete cached entries older than a duration",
		Example: `  tkn-act cache prune --older-than 168h`,
		RunE:    func(_ *cobra.Command, _ []string) error { return runCachePrune() },
	}
	pruneCmd.Flags().DurationVar(&cachePruneOlder, "older-than", 30*24*time.Hour,
		"delete entries older than this duration (default 30 days)")

	clearCmd := &cobra.Command{
		Use:     "clear",
		Short:   "Delete every cached entry (requires -y)",
		Example: `  tkn-act cache clear -y`,
		RunE:    func(_ *cobra.Command, _ []string) error { return runCacheClear() },
	}
	clearCmd.Flags().BoolVarP(&cacheYes, "yes", "y", false, "confirm non-interactive deletion")

	cmd.AddCommand(listCmd)
	cmd.AddCommand(pruneCmd)
	cmd.AddCommand(clearCmd)
	return cmd
}

// resolveCacheDir returns the active --resolver-cache-dir, falling back
// to the default $XDG_CACHE_HOME/tkn-act/resolved root used by `run`.
func resolveCacheDir() string {
	if gf.resolverCacheDir != "" {
		return gf.resolverCacheDir
	}
	return filepath.Join(cacheDir(), "resolved")
}

func runCacheList() error {
	root := resolveCacheDir()
	c := refresolver.NewDiskCache(root)
	entries, err := c.List()
	if err != nil {
		return exitcode.Wrap(exitcode.Generic, err)
	}
	if entries == nil {
		entries = []refresolver.CacheEntry{}
	}
	if gf.output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cacheListResult{Root: root, Entries: entries})
	}
	if len(entries) == 0 {
		fmt.Printf("(no cached entries under %s)\n", root)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESOLVER\tKEY\tSIZE\tAGE")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", e.Resolver, e.Key, e.Size, humanAge(e.ModTime))
	}
	return tw.Flush()
}

func runCachePrune() error {
	root := resolveCacheDir()
	c := refresolver.NewDiskCache(root)
	n, err := c.PruneOlderThan(cachePruneOlder)
	if err != nil {
		return exitcode.Wrap(exitcode.Generic, err)
	}
	if gf.output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cachePruneResult{
			Root:      root,
			OlderThan: cachePruneOlder.String(),
			Pruned:    n,
		})
	}
	fmt.Printf("pruned %d entries older than %s under %s\n", n, cachePruneOlder, root)
	return nil
}

func runCacheClear() error {
	if !cacheYes {
		return exitcode.Wrap(exitcode.Usage, fmt.Errorf("cache clear is destructive; pass -y to confirm"))
	}
	root := resolveCacheDir()
	c := refresolver.NewDiskCache(root)
	n, err := c.Clear()
	if err != nil {
		return exitcode.Wrap(exitcode.Generic, err)
	}
	if gf.output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cacheClearResult{Root: root, Cleared: n})
	}
	fmt.Printf("cleared %d entries from %s\n", n, root)
	return nil
}

// humanAge returns a short age string for `cache list` pretty output.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
