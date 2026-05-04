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
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// errStepRefDeferred signals to SubstituteAllowStepRefs that a placeholder
// should be left intact (because the docker backend will resolve it per-step
// later). Other resolution errors still bubble out.
var errStepRefDeferred = errors.New("step ref deferred")

type Context struct {
	Params       map[string]string            // string params
	ArrayParams  map[string][]string          // array params (only used by SubstituteArgs for [*] expansion)
	ObjectParams map[string]map[string]string // object params
	Results      map[string]map[string]string // task name → result name → value
	ContextVars  map[string]string            // dotted name → value (without "context." prefix)
	// StepResults are the results produced by earlier steps within the same
	// Task. Populated by the docker backend right before each step launches;
	// nil during the engine's task-level substitution pass.
	StepResults map[string]map[string]string
	// CurrentStep is the step being resolved; required to resolve
	// $(step.results.<name>.path) to /tekton/steps/<step>/results/<name>.
	CurrentStep string
}

// First char allows digits to align with RFC 1123 names (Tekton accepts
// e.g. `1stcheckout` as a PipelineTask name, so $(tasks.1stcheckout...)
// must match). The remaining class already covers the dotted/bracketed
// reference grammar; \w includes digits.
var refPat = regexp.MustCompile(`\$\(([a-zA-Z0-9][\w.\[\]\*-]*)\)`)

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

// SubstituteAllowStepRefs is the deferred-tolerant counterpart of Substitute.
// It leaves placeholders intact when the inner pass legitimately cannot
// resolve them yet:
//
//   - $(step.results.X.path) and $(steps.X.results.Y) — resolved by the
//     docker backend per-step right before each step launches.
//   - $(params.X) for any X not bound in ctx.Params/ObjectParams — the
//     StepAction inner pass populates only its own scoped params, so an
//     outer task-level $(params.<task-param>) survives to the outer pass.
//   - $(workspaces.<name>.path), $(context.<name>) without a binding,
//     $(tasks.X.results.Y) without an entry — same deferral rationale; the
//     outer engine.substituteSpec pass has the full Task scope and resolves
//     these moments later.
//
// Strictly malformed refs (a key that doesn't match any known scope shape)
// still error here, just as they do in plain Substitute.
func SubstituteAllowStepRefs(s string, ctx Context) (string, error) {
	const sentinel = "\x00DOLLAR\x00"
	s = strings.ReplaceAll(s, "$$", sentinel)

	var firstErr error
	out := refPat.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		key := refPat.FindStringSubmatch(m)[1]
		v, err := lookupAllowDefer(key, ctx)
		if err != nil {
			if errors.Is(err, errStepRefDeferred) {
				return m // leave placeholder for the outer pass / docker backend
			}
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
// $(params.x[*]) and $(tasks.X.results.Y[*]) — the entire arg is replaced
// by the array's elements. For task-result [*] references, the source
// string is JSON-decoded into []string before splicing (Tekton's on-disk
// shape for an array-of-strings result).
func SubstituteArgs(args []string, ctx Context) ([]string, error) {
	var out []string
	for _, a := range args {
		if isArrayStarRef(a) {
			vals, err := expandArrayStar(a, ctx)
			if err != nil {
				return nil, err
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

// SubstituteArgsAllowStepRefs is the AllowStepRefs counterpart of SubstituteArgs.
// Unknown $(params.x[*]) array refs are LEFT INTACT (deferred) so the outer
// pass can resolve them under the full Task scope; mirrors the deferral
// semantics of SubstituteAllowStepRefs for scalar refs.
func SubstituteArgsAllowStepRefs(args []string, ctx Context) ([]string, error) {
	var out []string
	for _, a := range args {
		if isArrayStarRef(a) {
			vals, err := expandArrayStar(a, ctx)
			if err != nil {
				// Defer to the outer pass — the inner pass only populates
				// StepAction-scoped params and lacks the cross-task results
				// map; outer task-level array params and
				// $(tasks.X.results.Y[*]) refs pass through as the literal
				// token for the outer pass to resolve.
				out = append(out, a)
				continue
			}
			out = append(out, vals...)
			continue
		}
		s, err := SubstituteAllowStepRefs(a, ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// isArrayStarRef now accepts BOTH $(params.<name>[*]) and
// $(tasks.<task>.results.<name>[*]). Matrix-fanned task-result
// aggregation depends on the latter.
func isArrayStarRef(s string) bool {
	if !strings.HasPrefix(s, "$(") || !strings.HasSuffix(s, "[*])") {
		return false
	}
	inner := s[2 : len(s)-len("[*])")]
	return strings.HasPrefix(inner, "params.") || strings.HasPrefix(inner, "tasks.")
}

// expandArrayStar resolves an `$(...[*])` reference into the
// per-element string slice it represents. Two recognised forms:
//
//	$(params.<name>[*])              — read from ctx.ArrayParams
//	$(tasks.<task>.results.<name>[*]) — read JSON-array literal
//	                                    from ctx.Results
func expandArrayStar(s string, ctx Context) ([]string, error) {
	inner := s[2 : len(s)-len("[*])")] // strip $( ... [*])
	if strings.HasPrefix(inner, "params.") {
		name := strings.TrimPrefix(inner, "params.")
		vals, ok := ctx.ArrayParams[name]
		if !ok {
			return nil, fmt.Errorf("unknown array param %q", name)
		}
		return vals, nil
	}
	if strings.HasPrefix(inner, "tasks.") {
		rest := strings.TrimPrefix(inner, "tasks.")
		dot := strings.Index(rest, ".results.")
		if dot < 0 {
			return nil, fmt.Errorf("malformed task-result [*] ref %q", s)
		}
		task := rest[:dot]
		name := strings.TrimPrefix(rest[dot:], ".results.")
		rs, ok := ctx.Results[task]
		if !ok {
			return nil, fmt.Errorf("no results for task %q", task)
		}
		raw, ok := rs[name]
		if !ok {
			return nil, fmt.Errorf("task %q has no result %q", task, name)
		}
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err != nil {
			return nil, fmt.Errorf("task result %q is not a JSON-array: %w", task+"."+name, err)
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unrecognised [*] reference %q", s)
}

// lookupAllowDefer is the deferral-tolerant counterpart of lookup. It calls
// lookup and then converts the four "unknown name in a known scope" errors
// into errStepRefDeferred so the AllowStepRefs caller leaves the literal
// $(...) placeholder intact for a subsequent pass to resolve. The four
// scopes that can be deferred:
//
//   - params.<name> not bound in ctx (the StepAction inner pass populates
//     only its own scoped params; outer params survive).
//   - workspaces.<name>.path (the inner StepAction pass has no workspace
//     scope; the outer task pass resolves these to /workspace/<name>).
//   - context.<name> not bound (e.g. context.taskRun.name is populated
//     only by the outer engine pass).
//   - tasks.<name>.results.<name> not bound (only the outer pass has the
//     accumulated cross-task results map).
//
// Malformed refs (e.g. tasks.X with no .results.Y suffix) still error.
func lookupAllowDefer(key string, ctx Context) (string, error) {
	v, err := lookup(key, ctx)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, errStepRefDeferred) {
		return "", err
	}
	switch {
	case strings.HasPrefix(key, "params."):
		// "unknown param" / "unknown object param" / "object param has no key"
		// all collapse into deferral here. The reference name is the only
		// thing the outer pass needs preserved.
		return "", errStepRefDeferred
	case strings.HasPrefix(key, "context."):
		return "", errStepRefDeferred
	case strings.HasPrefix(key, "tasks."):
		// Only well-formed tasks.X.results.Y can be deferred; lookup
		// already returns "malformed task ref" for the bad shape.
		parts := strings.SplitN(key, ".", 4)
		if len(parts) == 4 && parts[2] == "results" {
			return "", errStepRefDeferred
		}
		return "", err
	}
	return "", err
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
	case strings.HasPrefix(key, "results."):
		rest := strings.TrimPrefix(key, "results.")
		if strings.HasSuffix(rest, ".path") {
			name := strings.TrimSuffix(rest, ".path")
			return "/tekton/results/" + name, nil
		}
		return "", fmt.Errorf("unknown results ref %q", key)
	case strings.HasPrefix(key, "step.results."):
		// $(step.results.<name>.path) -> /tekton/steps/<current-step>/results/<name>
		rest := strings.TrimPrefix(key, "step.results.")
		if !strings.HasSuffix(rest, ".path") {
			return "", fmt.Errorf("unknown step.results ref %q", key)
		}
		if ctx.CurrentStep == "" {
			return "", errStepRefDeferred
		}
		name := strings.TrimSuffix(rest, ".path")
		return "/tekton/steps/" + ctx.CurrentStep + "/results/" + name, nil
	case strings.HasPrefix(key, "steps."):
		// $(steps.<step>.results.<name>) -> literal value of an earlier step's result
		parts := strings.SplitN(key, ".", 4)
		if len(parts) != 4 || parts[2] != "results" {
			return "", fmt.Errorf("malformed steps ref %q", key)
		}
		stepName, resName := parts[1], parts[3]
		if ctx.StepResults == nil {
			return "", errStepRefDeferred
		}
		if rs, ok := ctx.StepResults[stepName]; ok {
			if v, ok := rs[resName]; ok {
				return v, nil
			}
			return "", fmt.Errorf("step %q has no result %q", stepName, resName)
		}
		return "", fmt.Errorf("no results for step %q (must be a previous step in the same Task)", stepName)
	case strings.HasPrefix(key, "workspaces."):
		rest := strings.TrimPrefix(key, "workspaces.")
		if strings.HasSuffix(rest, ".path") {
			name := strings.TrimSuffix(rest, ".path")
			return "/workspace/" + name, nil
		}
		return "", fmt.Errorf("unknown workspaces ref %q", key)
	default:
		return "", fmt.Errorf("unknown reference $(%s)", key)
	}
}
