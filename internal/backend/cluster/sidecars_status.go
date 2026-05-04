package cluster

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SidecarStatus is a tkn-act-internal projection of one entry from
// taskRun.status.sidecars[]. Mirrors the subset of corev1.ContainerStatus
// fields the watch-loop needs to emit sidecar-start / sidecar-end.
type SidecarStatus struct {
	Name       string
	Container  string
	Running    bool
	Terminated bool
	ExitCode   int32
}

// parsePodSidecarStatuses reads taskRun.status.sidecars[] from an
// unstructured TaskRun and returns one SidecarStatus per entry.
// Returns nil if status.sidecars is missing, empty, or the input
// is nil.
//
// Pure function — factored out of run.go's per-TaskRun watch loop
// so the cluster sidecar-event emission can be unit-tested without
// the -tags cluster integration scaffolding (cluster integration
// tests don't count toward the per-package coverage gate).
func parsePodSidecarStatuses(tr *unstructured.Unstructured) []SidecarStatus {
	if tr == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(tr.Object, "status", "sidecars")
	if err != nil || !found {
		return nil
	}
	out := make([]SidecarStatus, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		s := SidecarStatus{}
		if v, ok := m["name"].(string); ok {
			s.Name = v
		}
		if v, ok := m["container"].(string); ok {
			s.Container = v
		}
		if _, ok := m["running"].(map[string]any); ok {
			s.Running = true
		}
		if t, ok := m["terminated"].(map[string]any); ok {
			s.Terminated = true
			switch v := t["exitCode"].(type) {
			case int64:
				s.ExitCode = int32(v)
			case float64:
				s.ExitCode = int32(v)
			case int:
				s.ExitCode = int32(v)
			case int32:
				s.ExitCode = v
			}
		}
		out = append(out, s)
	}
	return out
}
