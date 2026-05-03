package cluster

import "testing"

// TestMapPipelineRunStatus locks in the cross-backend status contract: the
// cluster watch must produce the same engine.RunResult.Status string the
// docker engine would for the same outcome.
func TestMapPipelineRunStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		reason  string
		want    string
	}{
		{"succeeded", "True", "Succeeded", "succeeded"},
		{"true ignores reason", "True", "PipelineRunTimeout", "succeeded"},
		{"plain failed", "False", "Failed", "failed"},
		{"empty reason still failed", "False", "", "failed"},
		{"pipelinerun timeout", "False", "PipelineRunTimeout", "timeout"},
		{"taskrun timeout", "False", "TaskRunTimeout", "timeout"},
		{"unknown reason still failed", "False", "InternalError", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapPipelineRunStatus(tc.status, tc.reason); got != tc.want {
				t.Errorf("mapPipelineRunStatus(%q,%q) = %q, want %q", tc.status, tc.reason, got, tc.want)
			}
		})
	}
}
