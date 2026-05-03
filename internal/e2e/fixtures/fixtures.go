// Package fixtures is the single source of truth for the e2e fixture set
// shared between the docker-backend harness (internal/e2e) and the cluster-
// backend harness (internal/clustere2e). Both harnesses iterate All() so any
// fixture added here automatically runs on both backends, with divergences
// captured per-fixture rather than by silently omitting tests on one side.
package fixtures

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
		{Dir: "step-results", Pipeline: "stepres", WantStatus: "succeeded"},
		{
			Dir: "volumes", Pipeline: "configmap-eater", WantStatus: "succeeded",
			ConfigMaps: map[string]map[string]string{
				"app-config": {"greeting": "hello-from-cm"},
			},
		},
	}
}
