// Package validator runs semantic checks on a loaded Bundle: refs resolve,
// the pipeline DAG has no cycles, workspaces are bound, params are present.
package validator

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/danielfbm/tkn-act/internal/engine/dag"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// Options carries optional knobs for resolver-aware validation. Zero
// value is fine; ValidateWithOptions(b, name, params, Options{}) is
// equivalent to Validate(b, name, params).
type Options struct {
	// Offline rejects any resolver-backed ref that isn't already in
	// the cache. The actual cache-presence check is delegated to
	// CacheCheck (nil means "always miss").
	Offline bool
	// RegisteredResolvers is the allow-list of resolver names that
	// dispatch in direct mode. Default empty means "no restriction"
	// — every non-empty resolver name is accepted (useful in tests).
	// Phase 1's CLI populates this from --resolver-allow.
	RegisteredResolvers []string
	// RemoteResolverEnabled short-circuits the direct-mode allow-list
	// check: when true, an arbitrary resolver name is accepted because
	// the remote cluster's controller knows it. Phase 5 flips this on
	// when --remote-resolver-context is set.
	RemoteResolverEnabled bool
	// CacheCheck answers "is this resolver request in the cache?"
	// for the --offline pre-flight. nil means "no cache wired" —
	// every offline check fails. Phase 6 wires the on-disk cache.
	CacheCheck func(UnresolvedRef) bool
}

// UnresolvedRef is the validator's view of a resolver-backed ref
// (mirrors loader.UnresolvedRef but lives here so the CacheCheck
// callback shape is stable).
type UnresolvedRef struct {
	Pipeline     string
	PipelineTask string
	Kind         string
	Resolver     string
	Params       map[string]string
}

// Validate checks the named pipeline against the bundle. providedParams names
// only — values are checked elsewhere. Returns all errors found, not just the
// first.
func Validate(b *loader.Bundle, pipelineName string, providedParams map[string]bool) []error {
	return ValidateWithOptions(b, pipelineName, providedParams, Options{})
}

// ValidateWithOptions is Validate plus resolver-aware checks driven by
// Options. The shared check pipeline runs unchanged; resolver checks
// run after, on the same pl.Spec.Tasks ∪ pl.Spec.Finally walk.
func ValidateWithOptions(b *loader.Bundle, pipelineName string, providedParams map[string]bool, opts Options) []error {
	errs := validateCore(b, pipelineName, providedParams)
	pl, ok := b.Pipelines[pipelineName]
	if !ok {
		return errs
	}
	// Resolver-aware checks. Walk both main tasks and finally.
	allow := map[string]struct{}{}
	for _, n := range opts.RegisteredResolvers {
		allow[n] = struct{}{}
	}
	knownTasks := map[string]struct{}{}
	for _, pt := range pl.Spec.Tasks {
		knownTasks[pt.Name] = struct{}{}
	}
	for _, pt := range pl.Spec.Finally {
		knownTasks[pt.Name] = struct{}{}
	}

	check := func(pt tektontypes.PipelineTask) {
		if pt.TaskRef == nil || pt.TaskRef.Resolver == "" {
			return
		}
		// Direct-mode allow-list check (skipped in remote mode).
		if !opts.RemoteResolverEnabled && len(opts.RegisteredResolvers) > 0 {
			if _, ok := allow[pt.TaskRef.Resolver]; !ok {
				errs = append(errs, fmt.Errorf("pipeline task %q: resolver %q is not in the allow-list (use --resolver-allow=%s,%s or --remote-resolver-context)",
					pt.Name, pt.TaskRef.Resolver, strings.Join(opts.RegisteredResolvers, ","), pt.TaskRef.Resolver))
			}
		}
		// resolver.params upstream-result reference check.
		params := map[string]string{}
		for _, p := range pt.TaskRef.ResolverParams {
			collectStrings(p.Value, func(s string) {
				for _, ref := range extractTaskRefs(s) {
					if _, ok := knownTasks[ref]; !ok {
						errs = append(errs, fmt.Errorf("pipeline task %q: resolver param %q references unknown task %q (must be in spec.tasks or spec.finally)",
							pt.Name, p.Name, ref))
					}
				}
			})
			// Best-effort literal capture for the cache-check callback.
			// The actual lookup happens at dispatch time after $(...)
			// substitution; here we feed pre-substitution params.
			if p.Value.Type == tektontypes.ParamTypeString || p.Value.Type == "" {
				params[p.Name] = p.Value.StringVal
			}
		}
		// --offline cache check.
		if opts.Offline {
			ref := UnresolvedRef{
				Pipeline: pipelineName, PipelineTask: pt.Name,
				Kind: "Task", Resolver: pt.TaskRef.Resolver,
				Params: params,
			}
			present := false
			if opts.CacheCheck != nil {
				present = opts.CacheCheck(ref)
			}
			if !present {
				errs = append(errs, fmt.Errorf("pipeline task %q: resolver %q cache miss while --offline is set", pt.Name, pt.TaskRef.Resolver))
			}
		}
	}
	for _, pt := range pl.Spec.Tasks {
		check(pt)
	}
	for _, pt := range pl.Spec.Finally {
		check(pt)
	}
	return errs
}

// validateCore is the original Validate body, refactored to share with
// ValidateWithOptions. It does NOT examine resolver-backed taskRefs
// (those are intentionally allowed to skip the "unknown Task" rule;
// the resolver layer fetches them at dispatch time).
func validateCore(b *loader.Bundle, pipelineName string, providedParams map[string]bool) []error {
	var errs []error

	pl, ok := b.Pipelines[pipelineName]
	if !ok {
		return []error{fmt.Errorf("pipeline %q not found in loaded files", pipelineName)}
	}

	// 1. Validate task refs and inline taskSpecs.
	all := append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...)
	all = append(all, pl.Spec.Finally...)
	resolvedTasks := map[string]tektontypes.TaskSpec{} // pipelineTaskName → resolved Task
	for _, pt := range all {
		switch {
		case pt.TaskRef != nil && pt.TaskSpec != nil:
			errs = append(errs, fmt.Errorf("pipeline task %q sets both taskRef and taskSpec", pt.Name))
		case pt.TaskRef != nil && pt.TaskRef.Resolver != "":
			// Resolver-backed taskRef: bytes aren't available at
			// validate time. Resolver-aware checks (allow-list, offline
			// cache, dangling result-refs in resolver.params) run from
			// ValidateWithOptions. The per-Task-spec checks (steps
			// non-empty, params bound, etc.) get applied at dispatch
			// time via validator.ValidateTaskSpec.
		case pt.TaskRef != nil:
			t, ok := b.Tasks[pt.TaskRef.Name]
			if !ok {
				errs = append(errs, fmt.Errorf("pipeline task %q references unknown Task %q", pt.Name, pt.TaskRef.Name))
				continue
			}
			resolvedTasks[pt.Name] = t.Spec
		case pt.TaskSpec != nil:
			resolvedTasks[pt.Name] = *pt.TaskSpec
		default:
			errs = append(errs, fmt.Errorf("pipeline task %q has no taskRef or taskSpec", pt.Name))
		}
	}

	// 2. Required params declared by tasks must be bound. Matrix params
	// (cross-product param names + every include row's params) count
	// as bound; the engine's expandMatrix pass merges them onto each
	// expansion's pt.Params.
	for _, pt := range all {
		spec, ok := resolvedTasks[pt.Name]
		if !ok {
			continue
		}
		bound := map[string]bool{}
		for _, p := range pt.Params {
			bound[p.Name] = true
		}
		if pt.Matrix != nil {
			for _, mp := range pt.Matrix.Params {
				bound[mp.Name] = true
			}
			for _, inc := range pt.Matrix.Include {
				for _, p := range inc.Params {
					bound[p.Name] = true
				}
			}
		}
		for _, decl := range spec.Params {
			if decl.Default == nil && !bound[decl.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q missing required param %q", pt.Name, decl.Name))
			}
		}
	}

	// 3. Workspaces declared by the task must be bound by the pipeline task.
	pipelineWS := map[string]bool{}
	for _, w := range pl.Spec.Workspaces {
		pipelineWS[w.Name] = true
	}
	for _, pt := range all {
		spec, ok := resolvedTasks[pt.Name]
		if !ok {
			continue
		}
		bound := map[string]string{}
		for _, b := range pt.Workspaces {
			bound[b.Name] = b.Workspace
		}
		for _, decl := range spec.Workspaces {
			if decl.Optional {
				continue
			}
			plws, ok := bound[decl.Name]
			if !ok {
				errs = append(errs, fmt.Errorf("pipeline task %q missing workspace binding %q", pt.Name, decl.Name))
				continue
			}
			if !pipelineWS[plws] {
				errs = append(errs, fmt.Errorf("pipeline task %q binds workspace %q to undeclared pipeline workspace %q", pt.Name, decl.Name, plws))
			}
		}
	}

	// 4. DAG: cycle + unknown runAfter.
	g := dag.New()
	main := map[string]bool{}
	for _, pt := range pl.Spec.Tasks {
		g.AddNode(pt.Name)
		main[pt.Name] = true
	}
	for _, pt := range pl.Spec.Tasks {
		for _, dep := range pt.RunAfter {
			if !main[dep] {
				errs = append(errs, fmt.Errorf("pipeline task %q runAfter references unknown task %q", pt.Name, dep))
				continue
			}
			g.AddEdge(dep, pt.Name)
		}
	}
	if _, err := g.Levels(); err != nil {
		errs = append(errs, fmt.Errorf("pipeline DAG: %w", err))
	}

	// 5. Finally task names must not collide with main DAG.
	finally := map[string]bool{}
	for _, pt := range pl.Spec.Finally {
		if main[pt.Name] {
			errs = append(errs, fmt.Errorf("finally task %q collides with main task name", pt.Name))
		}
		if finally[pt.Name] {
			errs = append(errs, fmt.Errorf("duplicate finally task %q", pt.Name))
		}
		finally[pt.Name] = true
	}

	// 6. When-expression operator sanity.
	for _, pt := range all {
		for _, w := range pt.When {
			op := strings.ToLower(w.Operator)
			if op != "in" && op != "notin" {
				errs = append(errs, fmt.Errorf("pipeline task %q: unsupported when operator %q (only 'in' and 'notin')", pt.Name, w.Operator))
			}
		}
	}

	// 7. Retries must be non-negative.
	for _, pt := range all {
		if pt.Retries < 0 {
			errs = append(errs, fmt.Errorf("pipeline task %q: retries must be non-negative, got %d", pt.Name, pt.Retries))
		}
	}

	// 8. Task timeout must parse as a Go duration.
	for name, spec := range resolvedTasks {
		if spec.Timeout == "" {
			continue
		}
		if _, err := time.ParseDuration(spec.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("pipeline task %q: invalid timeout %q: %v", name, spec.Timeout, err))
		}
	}

	// 8b. Pipeline-level timeouts: parseable, positive, and tasks+finally ≤ pipeline.
	if t := pl.Spec.Timeouts; t != nil {
		var pdur, tdur, fdur time.Duration
		var perr, terr, ferr error
		if t.Pipeline != "" {
			pdur, perr = parseTimeout("timeouts.pipeline", t.Pipeline)
			if perr != nil {
				errs = append(errs, perr)
			}
		}
		if t.Tasks != "" {
			tdur, terr = parseTimeout("timeouts.tasks", t.Tasks)
			if terr != nil {
				errs = append(errs, terr)
			}
		}
		if t.Finally != "" {
			fdur, ferr = parseTimeout("timeouts.finally", t.Finally)
			if ferr != nil {
				errs = append(errs, ferr)
			}
		}
		if perr == nil && terr == nil && ferr == nil &&
			pdur > 0 && tdur > 0 && fdur > 0 && tdur+fdur > pdur {
			errs = append(errs, fmt.Errorf(
				"timeouts.tasks (%s) + timeouts.finally (%s) > timeouts.pipeline (%s)",
				tdur, fdur, pdur))
		}
	}

	// 8c. Pipeline.spec.results: every $(tasks.X.results.Y) reference
	// must name a task that exists in spec.tasks ∪ spec.finally. Result-
	// name existence isn't checked here (some Tasks compute results
	// dynamically; resolution-time error handling drops unknown names
	// non-fatally). Result names themselves must also be unique — two
	// entries with the same name silently collide in the resolved map
	// and the user has no recovery path.
	if len(pl.Spec.Results) > 0 {
		known := map[string]bool{}
		for _, pt := range pl.Spec.Tasks {
			known[pt.Name] = true
		}
		for _, pt := range pl.Spec.Finally {
			known[pt.Name] = true
		}
		seenName := map[string]bool{}
		for _, r := range pl.Spec.Results {
			if seenName[r.Name] {
				errs = append(errs, fmt.Errorf("duplicate pipeline result name %q (each Pipeline.spec.results[].name must be unique)", r.Name))
			}
			seenName[r.Name] = true
			collectStrings(r.Value, func(s string) {
				for _, ref := range extractTaskRefs(s) {
					if !known[ref] {
						errs = append(errs, fmt.Errorf("pipeline result %q references unknown task %q (must be in spec.tasks or spec.finally)", r.Name, ref))
					}
				}
			})
		}
	}

	// 9. Step.OnError values must be empty, "continue", or "stopAndFail".
	for taskName, spec := range resolvedTasks {
		for _, st := range spec.Steps {
			switch st.OnError {
			case "", "continue", "stopAndFail":
			default:
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: unsupported onError %q (allowed: continue | stopAndFail)", taskName, st.Name, st.OnError))
			}
		}
	}

	// 10. Volume kinds: must be exactly one of emptyDir/hostPath/configMap/secret.
	for taskName, spec := range resolvedTasks {
		volNames := map[string]bool{}
		for _, v := range spec.Volumes {
			volNames[v.Name] = true
			n := 0
			if v.EmptyDir != nil {
				n++
			}
			if v.HostPath != nil {
				n++
			}
			if v.ConfigMap != nil {
				n++
			}
			if v.Secret != nil {
				n++
			}
			switch n {
			case 0:
				errs = append(errs, fmt.Errorf("pipeline task %q volume %q: unsupported volume kind (only emptyDir, hostPath, configMap, secret)", taskName, v.Name))
			case 1:
				// ok
			default:
				errs = append(errs, fmt.Errorf("pipeline task %q volume %q: multiple sources set on a single volume", taskName, v.Name))
			}
			if v.HostPath != nil && v.HostPath.Path == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q volume %q: hostPath.path is required", taskName, v.Name))
			}
		}
		// 11. Every volumeMount must reference a declared volume.
		for _, st := range spec.Steps {
			for _, vm := range st.VolumeMounts {
				if !volNames[vm.Name] {
					errs = append(errs, fmt.Errorf("pipeline task %q step %q: volumeMount %q references undeclared volume", taskName, st.Name, vm.Name))
				}
				if vm.MountPath == "" {
					errs = append(errs, fmt.Errorf("pipeline task %q step %q: volumeMount %q has empty mountPath", taskName, st.Name, vm.Name))
				}
			}
		}
	}

	// 12. Sidecars: name uniqueness within the Task (and against Step
	// names), image required, and every volumeMount must reference a
	// declared Task-level volume.
	for ptName, spec := range resolvedTasks {
		if len(spec.Sidecars) == 0 {
			continue
		}
		volumeNames := map[string]bool{}
		for _, v := range spec.Volumes {
			volumeNames[v.Name] = true
		}
		stepNames := map[string]bool{}
		for _, st := range spec.Steps {
			stepNames[st.Name] = true
		}
		seen := map[string]bool{}
		for _, sc := range spec.Sidecars {
			if sc.Name == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q sidecar has empty name", ptName))
				continue
			}
			if sc.Image == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q sidecar %q: image is required", ptName, sc.Name))
			}
			if seen[sc.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q has duplicate sidecar name %q", ptName, sc.Name))
			}
			seen[sc.Name] = true
			if stepNames[sc.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q sidecar %q collides with a step of the same name", ptName, sc.Name))
			}
			for _, vm := range sc.VolumeMounts {
				if !volumeNames[vm.Name] {
					errs = append(errs, fmt.Errorf("pipeline task %q sidecar %q volumeMount %q references undeclared Task volume", ptName, sc.Name, vm.Name))
				}
			}
		}
	}

	// 13 / 14 / 15 / 17 — these rule numbers continue past the existing
	// rule 12 (sidecars). The spec / plan numbering refers to the
	// StepActions ruleset (12-18); we keep that intent in error messages
	// but use unique rule numbers here to avoid collision with sidecars.
	//
	// Rules covered below:
	//   - resolver-form Step.ref rejection (spec rule 17)
	//   - ref + inline-body mutual exclusion (spec rule 13)
	//   - unknown StepAction reference (spec rule 12)
	//   - missing required StepAction param (spec rule 14)
	rawStepsByTask := loadRawSteps(b)
	for _, pt := range all {
		spec, ok := resolvedTasks[pt.Name]
		if !ok {
			continue
		}
		rawSteps := rawStepsByTask[taskNameForPT(b, pt)]
	stepLoop:
		for i, st := range spec.Steps {
			if st.Ref == nil {
				continue
			}
			// Resolver-form ref: rejection. Inspect the raw map view of
			// the ref: block — sigs.k8s.io/yaml drops unknown keys
			// (resolver / params / bundle) during typed unmarshaling, so
			// the typed StepActionRef alone can't detect them.
			if i < len(rawSteps) {
				if rawRef, ok := rawSteps[i]["ref"].(map[string]any); ok {
					for _, key := range []string{"resolver", "params", "bundle"} {
						if _, has := rawRef[key]; has {
							errs = append(errs, fmt.Errorf("pipeline task %q step %q: resolver-based StepAction refs (resolver/params/bundle under ref:) are not supported in this release; see Track 1 #9", pt.Name, st.Name))
							continue stepLoop
						}
					}
				}
			}
			// ref + inline body mutual exclusion.
			if st.Image != "" || len(st.Command) > 0 || len(st.Args) > 0 ||
				st.Script != "" || len(st.Env) > 0 || st.WorkingDir != "" ||
				st.ImagePullPolicy != "" || st.Resources != nil ||
				len(st.Results) > 0 {
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: ref and inline body are mutually exclusive", pt.Name, st.Name))
				continue
			}
			// Unknown StepAction reference.
			action, present := b.StepActions[st.Ref.Name]
			if !present {
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: references unknown StepAction %q", pt.Name, st.Name, st.Ref.Name))
				continue
			}
			// Required-param coverage.
			bound := map[string]bool{}
			for _, p := range st.Params {
				bound[p.Name] = true
			}
			for _, decl := range action.Spec.Params {
				if decl.Default == nil && !bound[decl.Name] {
					errs = append(errs, fmt.Errorf("pipeline task %q step %q: missing required StepAction param %q", pt.Name, st.Name, decl.Name))
				}
			}
		}
	}

	// 16. Inline Step (no ref:) must have a non-empty image after
	// stepTemplate inheritance. Run AFTER the validator's own
	// stepTemplate-merge pass so an image inherited from
	// Task.spec.stepTemplate.image counts. Only inline Steps
	// participate; ref-Steps inherit their image from the StepAction
	// body (rules 12-13 already caught the bad cases).
	for taskName, spec := range resolvedTasks {
		merged := mergeStepTemplateForImage(spec)
		for _, st := range merged.Steps {
			if st.Ref != nil {
				continue
			}
			if st.Image == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: inline step has no image (set image: or use ref:)", taskName, st.Name))
			}
		}
	}

	// 18. StepAction params with array/object defaults are rejected at
	// validate time (the inner pass only honors string defaults; this
	// prevents silent drops).
	for saName, sa := range b.StepActions {
		for _, decl := range sa.Spec.Params {
			if decl.Default == nil {
				continue
			}
			if t := decl.Default.Type; t == tektontypes.ParamTypeArray || t == tektontypes.ParamTypeObject {
				errs = append(errs, fmt.Errorf("StepAction %q param %q: default type %q is not supported (only string defaults)", saName, decl.Name, t))
			}
		}
	}

	// 19. Matrix rules. Mirrors Tekton's PipelineTask.matrix shape; cap
	// at validatorMatrixMaxRows (matches engine.matrixMaxRows). The
	// include-overlap rule (an include row's params overlapping a
	// matrix.params name) is the Critical-2 fix preventing
	// docker-vs-cluster divergence: real Tekton folds the include row
	// into the matching cross-product row; tkn-act always appends, so
	// we reject the overlap until v2 implements the fold.
	const validatorMatrixMaxRows = 256
	for _, pt := range all {
		if pt.Matrix == nil {
			continue
		}
		seen := map[string]bool{}
		cross := 0
		if len(pt.Matrix.Params) > 0 {
			cross = 1
		}
		for _, mp := range pt.Matrix.Params {
			if seen[mp.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q matrix declares param %q twice", pt.Name, mp.Name))
			}
			seen[mp.Name] = true
			if len(mp.Value) == 0 {
				errs = append(errs, fmt.Errorf("pipeline task %q matrix param %q must be a non-empty string list", pt.Name, mp.Name))
				continue
			}
			cross *= len(mp.Value)
		}
		total := cross + len(pt.Matrix.Include)
		if total > validatorMatrixMaxRows {
			errs = append(errs, fmt.Errorf("pipeline task %q matrix would produce %d rows, exceeding the cap of %d", pt.Name, total, validatorMatrixMaxRows))
		}
		crossNames := map[string]bool{}
		for _, mp := range pt.Matrix.Params {
			crossNames[mp.Name] = true
		}
		for _, inc := range pt.Matrix.Include {
			for _, p := range inc.Params {
				if p.Value.Type != tektontypes.ParamTypeString && p.Value.Type != "" {
					errs = append(errs, fmt.Errorf("pipeline task %q matrix include %q param %q must be a string", pt.Name, inc.Name, p.Name))
				}
				if crossNames[p.Name] {
					errs = append(errs, fmt.Errorf(
						"pipeline task %q matrix include %q param %q overlaps a cross-product param; "+
							"matrix.include params overlapping cross-product params are not supported in v1; "+
							"see Track 1 #3 follow-up",
						pt.Name, inc.Name, p.Name))
				}
			}
		}
		// Result-typing: matrix-fanned tasks may only emit string results.
		// Tekton promotes string → array-of-strings under the parent name;
		// array / object results have no such promotion. Reject so users
		// see the validation error before runtime.
		if spec, ok := resolvedTasks[pt.Name]; ok {
			for _, r := range spec.Results {
				if r.Type == "array" || r.Type == "object" {
					errs = append(errs, fmt.Errorf(
						"pipeline task %q (matrix-fanned) references task whose result %q is type %q; "+
							"matrix-fanned tasks may only emit string results",
						pt.Name, r.Name, r.Type))
				}
			}
		}
	}

	return errs
}

// taskNameForPT returns the bundle key for the underlying Task referenced
// by the PipelineTask, or "" for an inline taskSpec / resolver-backed ref.
// Used by rule 17 to look up the raw YAML (which is keyed by the loaded
// Task's metadata.name, not the PipelineTask's name).
func taskNameForPT(b *loader.Bundle, pt tektontypes.PipelineTask) string {
	if pt.TaskRef != nil && pt.TaskRef.Resolver == "" && pt.TaskRef.Name != "" {
		if _, ok := b.Tasks[pt.TaskRef.Name]; ok {
			return pt.TaskRef.Name
		}
	}
	return ""
}

// loadRawSteps re-unmarshals every loaded Task's raw bytes as a generic
// map and pulls out spec.steps[] as []map[string]any. Used by rule 17 to
// inspect Step.ref keys (resolver / params / bundle) that
// sigs.k8s.io/yaml silently drops during typed unmarshaling. Tasks
// without raw bytes (resolver-backed dispatches, inline taskSpec in
// PipelineTask) get an empty slice; rule 17 simply skips them.
func loadRawSteps(b *loader.Bundle) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	for name, raw := range b.RawTasks {
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		steps, _ := spec["steps"].([]any)
		if steps == nil {
			continue
		}
		ms := make([]map[string]any, 0, len(steps))
		for _, s := range steps {
			if m, ok := s.(map[string]any); ok {
				ms = append(ms, m)
			} else {
				ms = append(ms, nil)
			}
		}
		out[name] = ms
	}
	return out
}

// mergeStepTemplateForImage is a thin reimplementation of
// engine.applyStepTemplate's image-inheritance rule, scoped to what
// rule 16 needs (the validator can't depend on the engine package).
// Only Image inheritance from spec.stepTemplate.image into Steps with
// empty Image is applied; other fields are irrelevant for this check.
func mergeStepTemplateForImage(spec tektontypes.TaskSpec) tektontypes.TaskSpec {
	if spec.StepTemplate == nil || spec.StepTemplate.Image == "" {
		return spec
	}
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		ns := st
		if ns.Image == "" {
			ns.Image = spec.StepTemplate.Image
		}
		out.Steps[i] = ns
	}
	return out
}

// ValidateTaskSpec runs the per-Task semantic checks the engine needs
// after a resolver returns bytes (or after stepTemplate merge). It is a
// subset of the full Validate: only invariants that are intrinsic to a
// single TaskSpec (steps non-empty, timeout parses, onError values
// allowed, volumes well-formed). Pipeline-level checks (workspaces
// bound, params bound) are NOT here — those belong to Validate.
//
// This mirrors what the v1 admission webhook would reject. Phase 1 of
// resolvers calls this on resolver outputs to reject bytes that wouldn't
// pass a real Tekton install.
func ValidateTaskSpec(taskName string, spec tektontypes.TaskSpec) []error {
	var errs []error
	if len(spec.Steps) == 0 {
		errs = append(errs, fmt.Errorf("task %q: spec.steps: must have at least one step", taskName))
	}
	if spec.Timeout != "" {
		if _, err := time.ParseDuration(spec.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("task %q: invalid timeout %q: %v", taskName, spec.Timeout, err))
		}
	}
	for _, st := range spec.Steps {
		switch st.OnError {
		case "", "continue", "stopAndFail":
		default:
			errs = append(errs, fmt.Errorf("task %q step %q: unsupported onError %q (allowed: continue | stopAndFail)", taskName, st.Name, st.OnError))
		}
	}
	volNames := map[string]bool{}
	for _, v := range spec.Volumes {
		volNames[v.Name] = true
		n := 0
		if v.EmptyDir != nil {
			n++
		}
		if v.HostPath != nil {
			n++
		}
		if v.ConfigMap != nil {
			n++
		}
		if v.Secret != nil {
			n++
		}
		switch n {
		case 0:
			errs = append(errs, fmt.Errorf("task %q volume %q: unsupported volume kind (only emptyDir, hostPath, configMap, secret)", taskName, v.Name))
		case 1:
		default:
			errs = append(errs, fmt.Errorf("task %q volume %q: multiple sources set on a single volume", taskName, v.Name))
		}
		if v.HostPath != nil && v.HostPath.Path == "" {
			errs = append(errs, fmt.Errorf("task %q volume %q: hostPath.path is required", taskName, v.Name))
		}
	}
	for _, st := range spec.Steps {
		for _, vm := range st.VolumeMounts {
			if !volNames[vm.Name] {
				errs = append(errs, fmt.Errorf("task %q step %q: volumeMount %q references undeclared volume", taskName, st.Name, vm.Name))
			}
			if vm.MountPath == "" {
				errs = append(errs, fmt.Errorf("task %q step %q: volumeMount %q has empty mountPath", taskName, st.Name, vm.Name))
			}
		}
	}
	return errs
}

// parseTimeout parses a Tekton-style duration string and returns a
// non-zero positive duration. Empty strings should not reach this
// function. The error message includes the field name so users see
// "timeouts.pipeline: invalid duration" rather than just "invalid".
func parseTimeout(field, s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", field, s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive (use omission to mean no budget), got %q", field, s)
	}
	return d, nil
}

// taskResultRefPat matches $(tasks.<name>.results.<anything>) — we
// only need to extract the <name> for ref validation. RFC 1123 names
// allow leading digits (Tekton accepts e.g. "1stcheckout"), so the
// first char class spans `[a-zA-Z0-9]`, not `[a-zA-Z]`.
var taskResultRefPat = regexp.MustCompile(`\$\(tasks\.([a-zA-Z0-9][\w-]*)\.results\.[\w.-]+\)`)

// extractTaskRefs returns every task name referenced via
// $(tasks.X.results.Y) in s (in source order; duplicates allowed —
// the caller's known-set check is set-based anyway).
func extractTaskRefs(s string) []string {
	matches := taskResultRefPat.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// collectStrings calls fn once per string atom in v. For string-typed
// values that's the single StringVal; for array-typed, each element;
// for object-typed, each map value.
func collectStrings(v tektontypes.ParamValue, fn func(string)) {
	switch v.Type {
	case tektontypes.ParamTypeArray:
		for _, item := range v.ArrayVal {
			fn(item)
		}
	case tektontypes.ParamTypeObject:
		for _, item := range v.ObjectVal {
			fn(item)
		}
	default:
		fn(v.StringVal)
	}
}
