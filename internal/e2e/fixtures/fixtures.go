// Package fixtures is the single source of truth for the e2e fixture set
// shared between the docker-backend harness (internal/e2e) and the cluster-
// backend harness (internal/clustere2e). Both harnesses iterate All() so any
// fixture added here automatically runs on both backends, with divergences
// captured per-fixture rather than by silently omitting tests on one side.
package fixtures

import (
	"reflect"
)

// ResultsEqual compares two RunResult.Results-style maps for cross-
// backend fidelity, treating []string and []any-of-strings as equal
// and map[string]string vs map[string]any-of-strings as equal. The
// docker engine builds typed []string / map[string]string; fixture
// authors usually write []any literals. Either side may also be nil
// or empty when the other is — both mean "no resolved results."
func ResultsEqual(got, want map[string]any) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	if len(got) != len(want) {
		return false
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			return false
		}
		if !valueEqual(gv, wv) {
			return false
		}
	}
	return true
}

func valueEqual(g, w any) bool {
	// Strings: direct compare.
	if gs, ok := g.(string); ok {
		ws, ok := w.(string)
		return ok && gs == ws
	}
	// Arrays: normalise both sides to []string then DeepEqual.
	if gs, ok := toStringSlice(g); ok {
		ws, ok := toStringSlice(w)
		return ok && reflect.DeepEqual(gs, ws)
	}
	// Objects: normalise to map[string]string.
	if gm, ok := toStringMap(g); ok {
		wm, ok := toStringMap(w)
		return ok && reflect.DeepEqual(gm, wm)
	}
	return reflect.DeepEqual(g, w)
}

func toStringSlice(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func toStringMap(v any) (map[string]string, bool) {
	switch x := v.(type) {
	case map[string]string:
		return x, true
	case map[string]any:
		out := make(map[string]string, len(x))
		for k, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[k] = s
		}
		return out, true
	}
	return nil, false
}

// Fixture describes one testdata/e2e/<dir> case in a backend-agnostic way.
//
// WantStatus matches engine.RunResult.Status: succeeded | failed | timeout.
// Inline ConfigMaps / Secrets are seeded into both backends' volume stores
// (docker via volumes.Store.Add; cluster via the same Store, then projected
// into ephemeral kube ConfigMap/Secret resources at PipelineRun submit time).
//
// DockerOnly / ClusterOnly are the explicit divergence escape hatches: a
// fixture flagged DockerOnly is skipped by the cluster harness (and vice
// versa), and the reason should be stated in Description. After Track 2
// completes, every entry below is expected to leave both flags false.
type Fixture struct {
	// Dir under testdata/e2e (e.g. "hello", "volumes").
	Dir string
	// Pipeline is the pipeline name to run (a YAML may declare several).
	Pipeline string
	// Params for the pipeline run (key=value).
	Params map[string]string
	// WantStatus is the expected engine.RunResult.Status.
	WantStatus string
	// ConfigMaps maps name -> key -> inline value, seeded into the
	// configMap volumes.Store before the run.
	ConfigMaps map[string]map[string]string
	// Secrets is the same shape, seeded into the secret store.
	Secrets map[string]map[string]string
	// DockerOnly: skip in the cluster harness.
	DockerOnly bool
	// ClusterOnly: skip in the docker harness.
	ClusterOnly bool
	// Name is the Go subtest name used by t.Run; defaults to a derived form
	// when empty (see TestName).
	Name string
	// Description is a one-liner used in failure messages.
	Description string
	// WantResults, when non-nil, asserts engine.RunResult.Results is
	// equal (after a normalising pass — []string and []any with string
	// items count as equal, etc.) to this map. The cross-backend
	// fidelity guarantee for Pipeline.spec.results: both backends must
	// produce the same Results map for any fixture that sets this. If
	// nil, only WantStatus is asserted (the existing behavior).
	WantResults map[string]any
	// WantEventFields, if non-nil, asserts that for each named event
	// kind the first matching event in the captured stream carries
	// each named JSON-key/value pair. Shape:
	//   kind -> { jsonKey -> expectedValue }
	// Only the first event of each kind is inspected (run-start /
	// run-end always have exactly one; task-start / step-log are
	// asserted on the first emission). Skipped if the map is nil.
	WantEventFields map[string]map[string]string
}

// TestName returns the subtest name for this fixture: explicit Name when
// set, otherwise the directory + pipeline + sorted-params suffix so two
// fixtures over the same dir with different params get distinct names.
func (f Fixture) TestName() string {
	if f.Name != "" {
		return f.Name
	}
	n := f.Dir
	if f.Pipeline != "" && f.Pipeline != f.Dir {
		n += "_" + f.Pipeline
	}
	for k, v := range f.Params {
		n += "_" + k + "-" + v
	}
	return n
}

// All returns every shared e2e fixture. Order is stable across calls so
// failure logs line up between runs.
//
// Backend-divergent fixtures carry DockerOnly / ClusterOnly with a
// Description explaining why; the goal is to flip every flag to false as
// each backend gains the missing capability. As of the cross-backend-
// fidelity work, every entry below runs on both backends.
func All() []Fixture {
	return []Fixture{
		{Dir: "hello", Pipeline: "hello", WantStatus: "succeeded"},
		{Dir: "multilog", Pipeline: "multilog", WantStatus: "succeeded"},
		{Dir: "params-and-results", Pipeline: "chain", WantStatus: "succeeded"},
		{Dir: "workspaces", Pipeline: "ws-chain", WantStatus: "succeeded"},
		{
			Dir: "when-and-finally", Pipeline: "whens",
			Params: map[string]string{"env": "dev"}, WantStatus: "succeeded",
			Name: "when-and-finally_dev",
		},
		{
			Dir: "when-and-finally", Pipeline: "whens",
			Params: map[string]string{"env": "prod"}, WantStatus: "succeeded",
			Name: "when-and-finally_prod",
		},
		{Dir: "failure-propagation", Pipeline: "failprop", WantStatus: "failed"},
		{Dir: "onerror", Pipeline: "best-effort", WantStatus: "succeeded"},
		{Dir: "retries", Pipeline: "retries", WantStatus: "succeeded"},
		{Dir: "timeout", Pipeline: "hangs", WantStatus: "timeout"},
		{Dir: "pipeline-timeout", Pipeline: "pipeline-timeout", WantStatus: "timeout"},
		{Dir: "tasks-timeout", Pipeline: "tasks-timeout", WantStatus: "timeout"},
		{Dir: "finally-timeout", Pipeline: "finally-timeout", WantStatus: "timeout"},
		{Dir: "step-template", Pipeline: "step-template", WantStatus: "succeeded"},
		{
			Dir: "pipeline-results", Pipeline: "pipeline-results", WantStatus: "succeeded",
			// Both backends must surface the same resolved values. The
			// pipeline declares revision=$(tasks.checkout.results.commit)
			// and report=[checkout/commit, notify/id]; checkout emits
			// "abc123", notify (in finally) emits "notify-42".
			WantResults: map[string]any{
				"revision": "abc123",
				"report":   []any{"abc123", "notify-42"},
			},
		},
		{Dir: "step-results", Pipeline: "stepres", WantStatus: "succeeded"},
		{Dir: "step-actions", Pipeline: "step-actions", WantStatus: "succeeded"},
		{
			Dir: "display-name-description", Pipeline: "display-name-description", WantStatus: "succeeded",
			// WantEventFields asserts that specific event kinds carry the
			// documented display_name / description fields. Mirrors how
			// pipeline-results checks Results, but at the event-stream
			// layer.
			//
			// We intentionally do NOT assert on step-log here: the cluster
			// backend streams pod logs from goroutines that may not
			// capture anything for very fast pods (the watch on TaskRun
			// objects may miss the status-update event, or the
			// pod-logs-follow stream may attach after the pod has been
			// torn down). Step-log displayName plumbing is exercised by
			// the unit tests TestLogSinkStepLogPropagatesDisplayName and
			// TestStepDisplayNameLookup; the e2e harness asserts only the
			// run / task event-shape invariant.
			WantEventFields: map[string]map[string]string{
				"run-start":  {"display_name": "Build & test", "description": "Build then test."},
				"task-start": {"display_name": "Compile binary", "description": "Runs `go test ./...`."},
			},
		},
		{
			Dir: "volumes", Pipeline: "configmap-eater", WantStatus: "succeeded",
			ConfigMaps: map[string]map[string]string{
				"app-config": {"greeting": "hello-from-cm"},
			},
		},
		{Dir: "configmap-from-yaml", Pipeline: "configmap-from-yaml", WantStatus: "succeeded"},
		{Dir: "secret-from-yaml", Pipeline: "secret-from-yaml", WantStatus: "succeeded"},
		{Dir: "sidecars", Pipeline: "sidecars", WantStatus: "succeeded"},
		{
			Dir: "matrix", Pipeline: "matrix", WantStatus: "succeeded",
			// 2x2 cross-product (os ∈ {linux, darwin} × goversion ∈ {1.21, 1.22}).
			// Row order is outermost-iterates-slowest, so:
			//   build-0 = (linux, 1.21), build-1 = (linux, 1.22),
			//   build-2 = (darwin, 1.21), build-3 = (darwin, 1.22).
			// The pipeline declares an array result `tags` value
			// $(tasks.build.results.tag[*]) — the resolver splices the
			// per-expansion strings in row order.
			WantResults: map[string]any{
				"tags": []any{"linux-1.21", "linux-1.22", "darwin-1.21", "darwin-1.22"},
			},
			// DockerOnly: Tekton v0.65 (the pinned cluster-integration
			// version) does not surface matrix-fanned task results as
			// pipeline-level results when referenced via $(tasks.X.results.Y[*]),
			// even with enable-api-fields=alpha — the controller emits
			// CouldntGetPipelineResult: "the referenced results don't
			// exist". The matrix expansion itself + per-TaskRun
			// reconstruction via param-hash still works on cluster (every
			// expansion succeeds; events carry MatrixInfo); it's the
			// pipeline-level [*] aggregation that's missing controller-
			// side. Cluster-mode coverage of the per-TaskRun event path
			// is provided by internal/backend/cluster/run_test.go::
			// TestMatchMatrixRowFromTaskRun*. Track this to a Tekton bump
			// in cluster-integration.yml.
			DockerOnly:  true,
			Description: "matrix-fanned task results not exposed as pipeline results in Tekton v0.65 (alpha api fields)",
		},
		{
			Dir: "resolver-git", Pipeline: "resolver-git", WantStatus: "succeeded",
			// The repoURL is supplied by the harness: BuildBareRepoFromSeed
			// is invoked when the harness sees Dir=="resolver-git", and the
			// returned file:// URL is injected into Params["repoURL"]. See
			// internal/e2e/fixtures/resolvergit.go.
			Description: "direct git resolver via local bare repo (Track 1 #9 Phase 2)",
		},
		{
			Dir: "matrix-include", Pipeline: "matrix-include", WantStatus: "succeeded",
			// 1 cross-product row (os=linux, arch=amd64 from Task default)
			// + 2 include rows (arch=arm64 named arm-extra; arch=armv7
			// unnamed). Include rows inherit `os` from the Task default.
			// Order: cross-product, then include in declaration order.
			WantResults: map[string]any{
				"tags": []any{"linux-amd64", "linux-arm64", "linux-armv7"},
			},
			// DockerOnly: same Tekton v0.65 limitation as `matrix`. The
			// include row semantics also differ — Tekton v0.65 doesn't
			// emit TaskRuns for include-only-introduced params (rows
			// without an overlapping cross-product param). Bump Tekton
			// to a release where matrix-result PipelineResult resolution
			// is GA before flipping this back to cross-backend.
			DockerOnly:  true,
			Description: "matrix.include row TaskRun creation + matrix-fanned PipelineResult both gated on Tekton bump",
		},
	}
}
