package docker

import (
	"fmt"
)

// pauseImage is the per-Task netns owner. Tiny (~700KB), cached
// forever after first pull, blocks on pause(2) until killed. See
// the design spec §3.1 for provenance and rationale (chosen over
// "first-sidecar-as-netns-owner" so any sidecar can crash without
// taking the netns down).
const pauseImage = "gcr.io/google-containers/pause:3.9"

// sidecarStdout / sidecarStderr are the fine-grained Stream values
// emitted on EvtSidecarLog so consumers can filter sidecar logs
// from step logs. Stable contract — see AGENTS.md.
const (
	sidecarStdout = "sidecar-stdout"
	sidecarStderr = "sidecar-stderr"
)

// sidecarContainerName returns the Docker container name for a
// sidecar of the given task in the given run. Mirrors the step-name
// format with "sidecar-" interposed.
func sidecarContainerName(runID, taskRun, sidecarName string) string {
	return fmt.Sprintf("tkn-act-%s-%s-sidecar-%s", runID, taskRun, sidecarName)
}

// pauseContainerName returns the Docker container name for the
// per-Task pause container that owns the netns. Every sidecar and
// every step in the Task joins it via network_mode: container:<id>.
func pauseContainerName(runID, taskRun string) string {
	return fmt.Sprintf("tkn-act-%s-%s-pause", runID, taskRun)
}
