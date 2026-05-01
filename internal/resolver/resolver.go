// Package resolver performs Tekton-style variable substitution:
//   $(params.name)          – scalar param
//   $(params.obj.key)       – object param key
//   $(params.arr[*])        – array param expanded into multiple args (only via SubstituteArgs)
//   $(tasks.X.results.Y)    – named result from a previously-executed task
//   $(context.taskRun.name) – synthesized context var
//
// The double-dollar literal `$$` produces a single `$` and is not interpreted.
package resolver

import (
	"fmt"
	"regexp"
	"strings"
)

type Context struct {
	Params       map[string]string            // string params
	ArrayParams  map[string][]string          // array params (only used by SubstituteArgs for [*] expansion)
	ObjectParams map[string]map[string]string // object params
	Results      map[string]map[string]string // task name → result name → value
	ContextVars  map[string]string            // dotted name → value (without "context." prefix)
}

var refPat = regexp.MustCompile(`\$\(([a-zA-Z][\w.\[\]\*-]*)\)`)

// Substitute replaces $(...) references in s using ctx. Returns an error for
// unknown references.
func Substitute(s string, ctx Context) (string, error) {
	// Handle $$ escape first by replacing with a sentinel that contains no $,
	// then restoring after substitution.
	const sentinel = "\x00DOLLAR\x00"
	s = strings.ReplaceAll(s, "$$", sentinel)

	var firstErr error
	out := refPat.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		key := refPat.FindStringSubmatch(m)[1]
		v, err := lookup(key, ctx)
		if err != nil {
			firstErr = err
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return strings.ReplaceAll(out, sentinel, "$"), nil
}

// SubstituteArgs is like Substitute but operates on a []string and supports
// $(params.x[*]) — the entire arg is replaced by the array's elements.
func SubstituteArgs(args []string, ctx Context) ([]string, error) {
	var out []string
	for _, a := range args {
		if isArrayStarRef(a) {
			name := strings.TrimSuffix(strings.TrimPrefix(a, "$(params."), "[*])")
			vals, ok := ctx.ArrayParams[name]
			if !ok {
				return nil, fmt.Errorf("unknown array param %q", name)
			}
			out = append(out, vals...)
			continue
		}
		s, err := Substitute(a, ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func isArrayStarRef(s string) bool {
	return strings.HasPrefix(s, "$(params.") && strings.HasSuffix(s, "[*])")
}

func lookup(key string, ctx Context) (string, error) {
	switch {
	case strings.HasPrefix(key, "params."):
		rest := strings.TrimPrefix(key, "params.")
		if dot := strings.Index(rest, "."); dot >= 0 {
			obj := rest[:dot]
			k := rest[dot+1:]
			if om, ok := ctx.ObjectParams[obj]; ok {
				if v, ok := om[k]; ok {
					return v, nil
				}
				return "", fmt.Errorf("object param %q has no key %q", obj, k)
			}
			return "", fmt.Errorf("unknown object param %q", obj)
		}
		if v, ok := ctx.Params[rest]; ok {
			return v, nil
		}
		return "", fmt.Errorf("unknown param %q", rest)
	case strings.HasPrefix(key, "tasks."):
		// tasks.<task>.results.<name>
		parts := strings.SplitN(key, ".", 4)
		if len(parts) != 4 || parts[2] != "results" {
			return "", fmt.Errorf("malformed task ref %q", key)
		}
		task, name := parts[1], parts[3]
		if rs, ok := ctx.Results[task]; ok {
			if v, ok := rs[name]; ok {
				return v, nil
			}
			return "", fmt.Errorf("task %q has no result %q", task, name)
		}
		return "", fmt.Errorf("no results for task %q", task)
	case strings.HasPrefix(key, "context."):
		rest := strings.TrimPrefix(key, "context.")
		if v, ok := ctx.ContextVars[rest]; ok {
			return v, nil
		}
		return "", fmt.Errorf("unknown context var %q", rest)
	default:
		return "", fmt.Errorf("unknown reference $(%s)", key)
	}
}
