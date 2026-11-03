package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/discovery"
	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/validator"
)

// resolveInputFiles returns the YAML files `run` / `validate` should
// load. Positional arguments are treated exactly like additional `-f`
// values (the docs' own `tkn-act run pipeline.yaml` form): both sets
// are merged, `-f` first. Every positional path must exist and be a
// regular file — a typo must never silently fall back to discovering
// the current directory's pipeline.yaml. With neither `-f` nor a
// positional, the directory (default ".") is scanned via discovery.
func resolveInputFiles(flagFiles, positional []string, dir string) ([]string, error) {
	for _, p := range positional {
		st, err := os.Stat(p)
		if err != nil {
			// Same exit code (4) a missing -f file gets from the loader.
			return nil, exitcode.Wrap(exitcode.Validate, fmt.Errorf("read %s (positional arguments are treated as -f files): %w", p, err))
		}
		if st.IsDir() {
			return nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf("%q is a directory; use -C %s to discover Tekton YAML in it", p, p))
		}
	}
	files := append(append([]string{}, flagFiles...), positional...)
	if len(files) > 0 {
		return files, nil
	}
	if dir == "" {
		dir = "."
	}
	disc, err := discovery.Find(dir)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Usage, err)
	}
	return disc, nil
}

// selectPipeline picks the pipeline to act on: the explicit name when
// given, otherwise the only Pipeline in the bundle. Errors are usage
// errors (exit 2). The candidate list is sorted for stable messages.
func selectPipeline(b *loader.Bundle, name string) (string, error) {
	if name != "" {
		return name, nil
	}
	switch len(b.Pipelines) {
	case 0:
		return "", exitcode.Wrap(exitcode.Usage, fmt.Errorf("no Pipeline found in loaded files"))
	case 1:
		for n := range b.Pipelines {
			return n, nil
		}
	}
	names := make([]string, 0, len(b.Pipelines))
	for n := range b.Pipelines {
		names = append(names, n)
	}
	sort.Strings(names)
	return "", exitcode.Wrap(exitcode.Usage, fmt.Errorf("multiple pipelines loaded (%v); specify -p", names))
}

// parseParams parses repeated --param key=value values. Returns the
// value map and the set of provided names (the validator's
// providedParams view).
func parseParams(kvs []string) (map[string]tektontypes.ParamValue, map[string]bool, error) {
	vals := map[string]tektontypes.ParamValue{}
	names := map[string]bool{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf("--param expects key=value, got %q", kv))
		}
		vals[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
		names[k] = true
	}
	return vals, names, nil
}

// clusterResolverOptedIn reports whether the user opted into the
// off-by-default `cluster` resolver via either documented flag.
func clusterResolverOptedIn() bool {
	return gf.clusterResolverContext != "" || gf.clusterResolverKubeconfig != ""
}

// effectiveResolverAllow is --resolver-allow plus `cluster` when the
// user opted in through --cluster-resolver-context /
// --cluster-resolver-kubeconfig. Both the validator and the registry
// consume this list so the documented opt-in works end to end.
func effectiveResolverAllow() []string {
	out := append([]string{}, gf.resolverAllow...)
	if clusterResolverOptedIn() {
		for _, n := range out {
			if n == "cluster" {
				return out
			}
		}
		out = append(out, "cluster")
	}
	return out
}

// buildValidatorOptions assembles the resolver-aware validator options
// from the global flags. Shared by `run` and `validate` so the two
// commands agree on what is valid (allow-list, --offline cache).
func buildValidatorOptions(diskCache *refresolver.DiskCache) validator.Options {
	return validator.Options{
		Offline:               gf.offline,
		RegisteredResolvers:   effectiveResolverAllow(),
		RemoteResolverEnabled: gf.remoteResolverContext != "",
		CacheCheck: func(ref validator.UnresolvedRef) bool {
			return diskCache.Has(refresolver.Request{
				Resolver: ref.Resolver,
				Params:   ref.Params,
			})
		},
	}
}

// reportRunValidationErrors surfaces `run`'s pre-flight validation
// failures. Pretty mode keeps the historical `error: <msg>` lines on
// stderr; with -o json each failure is additionally emitted as an
// `error` event on stdout so agents parsing the event stream see why
// the run never started (instead of an empty stream).
func reportRunValidationErrors(stdout, stderr io.Writer, pipe string, errs []error) {
	var jrep reporter.Reporter
	if gf.output == "json" {
		jrep = reporter.NewJSON(stdout)
		defer func() { _ = jrep.Close() }()
	}
	for _, e := range errs {
		fmt.Fprintln(stderr, "error:", e)
		if jrep != nil {
			jrep.Emit(reporter.Event{
				Kind:     reporter.EvtError,
				Time:     time.Now(),
				Pipeline: pipe,
				Message:  "validation: " + e.Error(),
			})
		}
	}
}
