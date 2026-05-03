package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestTaskRunToOutcomeWithRetries locks in the cluster→engine contract for
// the retries fixture: a TaskRun whose status.retriesStatus has length 2
// (two failed attempts before a final success) must surface as Attempts=3
// with two RetryAttempt entries the engine then turns into task-retry
// events.
func TestTaskRunToOutcomeWithRetries(t *testing.T) {
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": "p-12345678-t-pod",
			"labels": map[string]any{
				"tekton.dev/pipelineTask": "t",
				"tekton.dev/pipelineRun":  "p-12345678",
			},
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":    "Succeeded",
					"status":  "True",
					"reason":  "Succeeded",
					"message": "All Steps have completed executing",
				},
			},
			"retriesStatus": []any{
				map[string]any{
					"conditions": []any{
						map[string]any{
							"type":    "Succeeded",
							"status":  "False",
							"reason":  "Failed",
							"message": "step-try exited with code 1",
						},
					},
				},
				map[string]any{
					"conditions": []any{
						map[string]any{
							"type":    "Succeeded",
							"status":  "False",
							"reason":  "Failed",
							"message": "step-try exited with code 1",
						},
					},
				},
			},
		},
	}}

	oc := taskRunToOutcome(tr)
	if oc.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", oc.Status)
	}
	if oc.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", oc.Attempts)
	}
	if got := len(oc.RetryAttempts); got != 2 {
		t.Fatalf("retry-attempts len = %d, want 2", got)
	}
	for i, r := range oc.RetryAttempts {
		if r.Attempt != i+1 {
			t.Errorf("retry[%d].Attempt = %d, want %d", i, r.Attempt, i+1)
		}
		if r.Status != "failed" {
			t.Errorf("retry[%d].Status = %q, want failed", i, r.Status)
		}
		if r.Message == "" {
			t.Errorf("retry[%d] missing message", i)
		}
	}
}

// TestTaskRunToOutcomeNoRetries: a happy-path TaskRun with no
// retriesStatus must report Attempts=1 and an empty RetryAttempts.
func TestTaskRunToOutcomeNoRetries(t *testing.T) {
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"tekton.dev/pipelineTask": "t"}},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Succeeded", "status": "True", "reason": "Succeeded"},
			},
		},
	}}
	oc := taskRunToOutcome(tr)
	if oc.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", oc.Attempts)
	}
	if oc.RetryAttempts != nil {
		t.Errorf("retry-attempts = %v, want nil", oc.RetryAttempts)
	}
	if oc.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", oc.Status)
	}
}

// TestTaskRunToOutcomeTimeout: a TaskRun ending with Reason: TaskRunTimeout
// must map to status "timeout" so the cluster engine emits the same
// task-end status the docker engine would for the timeout fixture.
func TestTaskRunToOutcomeTimeout(t *testing.T) {
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"tekton.dev/pipelineTask": "t"}},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":    "Succeeded",
					"status":  "False",
					"reason":  "TaskRunTimeout",
					"message": "TaskRun \"t\" failed to finish within \"3s\"",
				},
			},
		},
	}}
	oc := taskRunToOutcome(tr)
	if oc.Status != "timeout" {
		t.Errorf("status = %q, want timeout", oc.Status)
	}
}
