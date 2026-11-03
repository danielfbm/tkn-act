// Package resolver performs Tekton-style variable substitution:
//
//	$(params.name)          – scalar param
//	$(params.obj.key)       – object param key
//	$(params.arr[*])        – array param expanded into multiple args (only via SubstituteArgs)
//	$(params.arr[N])        – single array-param element
//	$(tasks.X.results.Y)    – named result from a previously-executed task
//	$(tasks.X.results.Y[N]) – element of an array result (JSON-decoded)
//	$(tasks.X.results.Y.k)  – key of an object result (JSON-decoded)
//	$(tasks.status)         – aggregate main-task status (finally tasks only)
//	$(tasks.X.status)       – per-pipeline-task status (finally tasks only)
//	$(context.taskRun.name) – synthesized context var
//	$(workspaces.W.bound)   – "true" / "false"
//
// References whose first segment is not a recognised Tekton namespace
// (params, tasks, context, workspaces, results, steps, step) are left
// untouched, exactly like Tekton: `$(date)` in a script is shell command
// substitution, not a Tekton variable.
//
// The double-dollar literal `$$` produces a single `$` and is not interpreted.
package resolver

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// errStepRefDeferred signals to SubstituteAllowStepRefs that a placeholder
// should be left intact (because the docker backend will resolve it per-step
// later). Other resolution errors still bubble out.
var errStepRefDeferred = errors.New("step ref deferred")

// errLeaveLiteral signals that the $(...) token is not a Tekton reference
// at all (unknown namespace, e.g. shell `$(date)`) and must be emitted
// verbatim by every substitution mode.
var errLeaveLiteral = errors.New("not a tekton reference")

// knownNamespaces is the set of first segments tkn-act interprets. Any
// other `$(name...)` token is left literal (Tekton semantics).
var knownNamespaces = map[string]bool{
	"params":     true,
	"tasks":      true,
	"context":    true,
	"workspaces": true,
	"results":    true,
	"steps":      true,
	"step":       true,
}

// indexSuffix matches a trailing `[N]` element index.
var indexSuffix = regexp.MustCompile(`^(.+)\[(\d+)\]$`)

// arrayStarRefPat matches a string that is exactly ONE whole-array
// reference: $(params.<name>[*]) or $(tasks.<t>.results.<name>[*]).
var arrayStarRefPat = regexp.MustCompile(`^\$\((?:params|tasks)\.[^()$\s]*\[\*\]\)$`)

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
	// BoundWorkspaces maps a Task workspace name to whether the
	// PipelineTask bound it; drives $(workspaces.<name>.bound). nil
	// means "binding info unavailable" (the ref then errors in strict
	// mode and is deferred by SubstituteAllowStepRefs).
	BoundWorkspaces map[string]bool
	// TasksStatus is $(tasks.status) — the aggregate execution status
	// of the main (non-finally) tasks. TaskStatuses is
	// $(tasks.<pipelineTask>.status). Both are populated only while
	// resolving a finally task; TaskStatuses == nil means "not in a
	// finally task" and both refs error, matching Tekton.
	TasksStatus  string
	TaskStatuses map[string]string
}

// substMode selects which lookup errors a substitution pass tolerates.
type substMode int

const (
	modeStrict    substMode = iota // every lookup error is fatal
	modeDeferStep                  // step-scoped refs left intact for the docker backend
	modeDeferAll                   // StepAction inner pass: unknown names in known scopes deferred
)

func substitute(s string, ctx Context, mode substMode) (string, error) {
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
		var v string
		var err error
		if mode == modeDeferAll {
			v, err = lookupAllowDefer(key, ctx)
		} else {
			v, err = lookup(key, ctx)
		}
		if err != nil {
			if errors.Is(err, errLeaveLiteral) {
				return m
			}
			if mode != modeStrict && errors.Is(err, errStepRefDeferred) {
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

// First char allows digits to align with RFC 1123 names (Tekton accepts
// e.g. `1stcheckout` as a PipelineTask name, so $(tasks.1stcheckout...)
// must match). The remaining class already covers the dotted/bracketed
// reference grammar; \w includes digits.
var refPat = regexp.MustCompile(`\$\(([a-zA-Z0-9][\w.\[\]\*-]*)\)`)

// Substitute replaces $(...) references in s using ctx. Returns an error for
// unknown references.
func Substitute(s string, ctx Context) (string, error) {
	return substitute(s, ctx, modeStrict)
}

// SubstituteDeferStepRefs resolves every reference against the full Task
// scope like Substitute, except that step-scoped refs
// ($(step.results.X.path), $(steps.X.results.Y)) are left intact for the
// docker backend's per-step pass. This is the mode the engine uses for
// the final task-level pass over Step fields: anything other than a
// step-scoped ref that can't be resolved is an error, never silently
// dropped or deferred to a backend that can't resolve it either.
func SubstituteDeferStepRefs(s string, ctx Context) (string, error) {
	return substitute(s, ctx, modeDeferStep)
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
	return substitute(s, ctx, modeDeferAll)
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

// SubstituteArgsDeferStepRefs is the DeferStepRefs counterpart of
// SubstituteArgs: whole-array `[*]` items are expanded strictly, every
// other item goes through SubstituteDeferStepRefs.
func SubstituteArgsDeferStepRefs(args []string, ctx Context) ([]string, error) {
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
		s, err := SubstituteDeferStepRefs(a, ctx)
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
//
// The string must be exactly one reference: `$(params.a[*]) $(params.b[*])`
// or `pre $(params.a[*])` are not whole-array refs.
func isArrayStarRef(s string) bool {
	return arrayStarRefPat.MatchString(s)
}

// IsArrayStarRef reports whether s is exactly one whole-array reference
// ($(params.<name>[*]) or $(tasks.<t>.results.<name>[*])). The engine
// uses it to bind such a string-typed PipelineTask param value as an
// array param (Tekton's array pass-through).
func IsArrayStarRef(s string) bool { return isArrayStarRef(s) }

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
// lookup and then converts the "unknown name in a known scope" errors
// into errStepRefDeferred so the AllowStepRefs caller leaves the literal
// $(...) placeholder intact for a subsequent pass to resolve. The scopes
// that can be deferred:
//
//   - params.<name> not bound in ctx (the StepAction inner pass populates
//     only its own scoped params; outer params survive).
//   - workspaces.<name>.bound without binding info (the inner StepAction
//     pass has no workspace scope; the outer task pass resolves these).
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
	if errors.Is(err, errStepRefDeferred) || errors.Is(err, errLeaveLiteral) {
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
	case strings.HasPrefix(key, "workspaces.") && strings.HasSuffix(key, ".bound") && ctx.BoundWorkspaces == nil:
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

// namespaceOf returns the first segment of a reference key (up to the
// first '.' or '[').
func namespaceOf(key string) string {
	if i := strings.IndexAny(key, ".["); i >= 0 {
		return key[:i]
	}
	return key
}

func lookup(key string, ctx Context) (string, error) {
	if !knownNamespaces[namespaceOf(key)] {
		// Not a Tekton variable (shell `$(date)`, `$(pwd)`, ...):
		// Tekton leaves these untouched and so do we.
		return "", errLeaveLiteral
	}
	switch {
	case strings.HasPrefix(key, "params."):
		return lookupParam(strings.TrimPrefix(key, "params."), ctx)
	case strings.HasPrefix(key, "tasks."):
		return lookupTasks(key, ctx)
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
		return lookupWorkspace(key, ctx)
	default:
		return "", fmt.Errorf("unknown reference $(%s)", key)
	}
}

// errWholeArray is the error for a `[*]` reference used inside a string.
// Tekton only accepts `[*]` as an isolated array element (an args /
// command item, an array param value, a when.values item).
func errWholeArray(key string) error {
	return fmt.Errorf("$(%s) is a whole-array reference: it must be used on its own as an array element (args/command item, array param value, when.values item), not embedded in a string", key)
}

// lookupParam resolves the part after "params.": a scalar name, an
// array element `name[N]`, or an object key `name.key`.
func lookupParam(rest string, ctx Context) (string, error) {
	if strings.HasSuffix(rest, "[*]") {
		return "", errWholeArray("params." + rest)
	}
	if v, ok := ctx.Params[rest]; ok {
		return v, nil
	}
	if m := indexSuffix.FindStringSubmatch(rest); m != nil {
		arr, ok := ctx.ArrayParams[m[1]]
		if !ok {
			return "", fmt.Errorf("unknown array param %q", m[1])
		}
		return indexInto(arr, m[2], "param "+strconv.Quote(m[1]))
	}
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
	return "", fmt.Errorf("unknown param %q", rest)
}

// lookupTasks resolves $(tasks.status), $(tasks.<t>.status) and
// $(tasks.<t>.results.<name>[N|.key]).
func lookupTasks(key string, ctx Context) (string, error) {
	rest := strings.TrimPrefix(key, "tasks.")
	if rest == "status" {
		if ctx.TaskStatuses == nil {
			return "", fmt.Errorf("$(tasks.status) is only available in finally tasks")
		}
		return ctx.TasksStatus, nil
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) == 2 && parts[1] == "status" {
		if ctx.TaskStatuses == nil {
			return "", fmt.Errorf("$(%s) is only available in finally tasks", key)
		}
		if v, ok := ctx.TaskStatuses[parts[0]]; ok {
			return v, nil
		}
		return "", fmt.Errorf("$(%s): unknown pipeline task %q (must be a task in spec.tasks)", key, parts[0])
	}
	if len(parts) != 3 || parts[1] != "results" {
		return "", fmt.Errorf("malformed task ref %q", key)
	}
	return lookupTaskResult(parts[0], parts[2], ctx)
}

// lookupTaskResult resolves `<name>`, `<name>[N]` (JSON-array result) or
// `<name>.<key>` (JSON-object result) against task's results.
func lookupTaskResult(task, sel string, ctx Context) (string, error) {
	rs, ok := ctx.Results[task]
	if !ok {
		return "", fmt.Errorf("no results for task %q", task)
	}
	if v, ok := rs[sel]; ok {
		return v, nil
	}
	if strings.HasSuffix(sel, "[*]") {
		return "", errWholeArray("tasks." + task + ".results." + sel)
	}
	if m := indexSuffix.FindStringSubmatch(sel); m != nil {
		raw, ok := rs[m[1]]
		if !ok {
			return "", fmt.Errorf("task %q has no result %q", task, m[1])
		}
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err != nil {
			return "", fmt.Errorf("task result %q is not a JSON array: %w", task+"."+m[1], err)
		}
		return indexInto(arr, m[2], "result "+strconv.Quote(task+"."+m[1]))
	}
	if dot := strings.Index(sel, "."); dot >= 0 {
		name, k := sel[:dot], sel[dot+1:]
		raw, ok := rs[name]
		if !ok {
			return "", fmt.Errorf("task %q has no result %q", task, name)
		}
		var obj map[string]string
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return "", fmt.Errorf("task result %q is not a JSON object: %w", task+"."+name, err)
		}
		v, ok := obj[k]
		if !ok {
			return "", fmt.Errorf("task result %q has no key %q", task+"."+name, k)
		}
		return v, nil
	}
	return "", fmt.Errorf("task %q has no result %q", task, sel)
}

// lookupWorkspace resolves $(workspaces.<name>.path|bound|claim|volume).
// `.path` is the docker backend's mount convention; `.claim` is always
// empty (tkn-act binds host directories, never PVCs) and `.volume` is
// the workspace name.
func lookupWorkspace(key string, ctx Context) (string, error) {
	rest := strings.TrimPrefix(key, "workspaces.")
	dot := strings.LastIndex(rest, ".")
	if dot < 0 {
		return "", fmt.Errorf("unknown workspaces ref %q", key)
	}
	name, attr := rest[:dot], rest[dot+1:]
	switch attr {
	case "path":
		return "/workspace/" + name, nil
	case "bound":
		if ctx.BoundWorkspaces == nil {
			return "", fmt.Errorf("$(%s): workspace binding info unavailable here", key)
		}
		if ctx.BoundWorkspaces[name] {
			return "true", nil
		}
		return "false", nil
	case "claim":
		return "", nil
	case "volume":
		return name, nil
	}
	return "", fmt.Errorf("unknown workspaces ref %q", key)
}

func indexInto(arr []string, idx, what string) (string, error) {
	n, err := strconv.Atoi(idx)
	if err != nil {
		return "", fmt.Errorf("bad index %q for %s", idx, what)
	}
	if n < 0 || n >= len(arr) {
		return "", fmt.Errorf("index %d out of range for %s (length %d)", n, what, len(arr))
	}
	return arr[n], nil
}
