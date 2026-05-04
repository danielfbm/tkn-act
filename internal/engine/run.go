package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// PipelineInput names what to run and how.
type PipelineInput struct {
	Bundle     *loader.Bundle
	Name       string                            // pipeline name
	Params     map[string]tektontypes.ParamValue // user-provided params
	Workspaces map[string]string                 // pipeline workspace name → host path
	RunID      string                            // optional; auto-generated if empty
}

// RunResult is the outcome of a pipeline run.
type RunResult struct {
	Status string // succeeded | failed
	Tasks  map[string]TaskOutcome
	// Reason is the backend-supplied terminal reason. On the cluster
	// backend this is the Tekton condition reason verbatim
	// (PipelineRunTimeout, PipelineValidationFailed, Failed, …); on
	// the docker backend it's empty. Surfaced for diagnostic logging
	// only — the user-visible status enum lives in Status.
	Reason string
	// Message is the backend-supplied terminal message. Same purpose
	// as Reason — surfaced so failure logs can attribute a misclassified
	// run to a specific backend code path.
	Message string
	// Results holds resolved Pipeline.spec.results once the run is
	// terminal. Each value is one of: string, []string, map[string]string.
	// nil or empty when the Pipeline didn't declare spec.results, or when
	// none resolved (every referenced task failed or skipped the result).
	// A dropped entry surfaces as an EvtError on the event stream — on
	// both the docker and the cluster backend.
	Results map[string]any
}

type TaskOutcome struct {
	Status   string
	Message  string
	Results  map[string]string
	Attempt  int           // final attempt number (1-based); 0 = did not run
	Duration time.Duration // duration of the final attempt
}

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// flattenStringParams reduces ParamValue map → simple string map for use as
// resolver context. Arrays/objects are kept in separate maps.
func flattenStringParams(in map[string]tektontypes.ParamValue) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if v.Type == tektontypes.ParamTypeString || v.Type == "" {
			out[k] = v.StringVal
		}
	}
	return out
}

func arrayParams(in map[string]tektontypes.ParamValue) map[string][]string {
	out := map[string][]string{}
	for k, v := range in {
		if v.Type == tektontypes.ParamTypeArray {
			out[k] = v.ArrayVal
		}
	}
	return out
}

func objectParams(in map[string]tektontypes.ParamValue) map[string]map[string]string {
	out := map[string]map[string]string{}
	for k, v := range in {
		if v.Type == tektontypes.ParamTypeObject {
			out[k] = v.ObjectVal
		}
	}
	return out
}

// applyDefaults fills missing string params from declared defaults.
func applyDefaults(declared []tektontypes.ParamSpec, given map[string]tektontypes.ParamValue) (map[string]tektontypes.ParamValue, error) {
	out := map[string]tektontypes.ParamValue{}
	for k, v := range given {
		out[k] = v
	}
	for _, d := range declared {
		if _, ok := out[d.Name]; ok {
			continue
		}
		if d.Default == nil {
			return nil, fmt.Errorf("param %q is required", d.Name)
		}
		out[d.Name] = *d.Default
	}
	return out, nil
}
