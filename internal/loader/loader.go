// Package loader parses one or more Tekton YAML files into a Bundle of typed
// resources keyed by name. Multi-document YAML is supported.
package loader

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"sigs.k8s.io/yaml"
)

// Bundle holds all resources loaded from one or more files.
type Bundle struct {
	Tasks        map[string]tektontypes.Task
	Pipelines    map[string]tektontypes.Pipeline
	PipelineRuns []tektontypes.PipelineRun // ordered as found
	TaskRuns     []tektontypes.TaskRun
	// StepActions are referenceable Step shapes (apiVersion
	// tekton.dev/v1beta1, kind StepAction). The engine inlines them
	// into Steps that carry `ref:` before stepTemplate / substitution.
	StepActions map[string]tektontypes.StepAction
	// ConfigMaps and Secrets are bytes-by-key, populated from any
	// `kind: ConfigMap` / `kind: Secret` (apiVersion: v1) doc found in
	// the loaded YAML. They are intended to be poured into the
	// volumes.Store at run time, where the precedence layering with
	// inline (--configmap) and on-disk (--configmap-dir) overrides
	// happens. Map shape: name -> key -> bytes.
	ConfigMaps map[string]map[string][]byte
	Secrets    map[string]map[string][]byte
}

// LoadFiles loads every resource from the given file paths, returning a merged
// Bundle. Duplicate names within the same kind are an error.
func LoadFiles(paths []string) (*Bundle, error) {
	out := &Bundle{
		Tasks:       map[string]tektontypes.Task{},
		Pipelines:   map[string]tektontypes.Pipeline{},
		StepActions: map[string]tektontypes.StepAction{},
		ConfigMaps:  map[string]map[string][]byte{},
		Secrets:     map[string]map[string][]byte{},
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		b, err := LoadBytes(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if err := merge(out, b); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LoadBytes parses one byte slice (possibly multi-doc) into a Bundle.
func LoadBytes(data []byte) (*Bundle, error) {
	out := &Bundle{
		Tasks:       map[string]tektontypes.Task{},
		Pipelines:   map[string]tektontypes.Pipeline{},
		StepActions: map[string]tektontypes.StepAction{},
		ConfigMaps:  map[string]map[string][]byte{},
		Secrets:     map[string]map[string][]byte{},
	}

	docs, err := splitYAMLDocs(data)
	if err != nil {
		return nil, err
	}
	for i, doc := range docs {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		if err := loadOne(out, doc); err != nil {
			return nil, fmt.Errorf("doc %d: %w", i+1, err)
		}
	}
	return out, nil
}

func loadOne(out *Bundle, data []byte) error {
	var head tektontypes.Object
	if err := yaml.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("parse head: %w", err)
	}
	switch head.APIVersion {
	case "tekton.dev/v1":
		// fall through to the Tekton-kind switch below (handles Task,
		// Pipeline, PipelineRun, TaskRun)
	case "tekton.dev/v1beta1":
		// v1beta1 is StepAction-only in this release. Returns directly:
		// the second switch below is skipped because Go's switch does
		// not fall through.
		switch head.Kind {
		case "StepAction":
			return loadStepAction(out, data)
		default:
			return fmt.Errorf("unsupported tekton.dev/v1beta1 kind %q (only StepAction)", head.Kind)
		}
	case "v1":
		// Core Kubernetes kinds we accept: ConfigMap, Secret.
		switch head.Kind {
		case "ConfigMap":
			return loadConfigMap(out, data)
		case "Secret":
			return loadSecret(out, data)
		default:
			return fmt.Errorf("unsupported v1 kind %q (only ConfigMap and Secret accepted at apiVersion v1)", head.Kind)
		}
	default:
		return fmt.Errorf("unsupported apiVersion %q (only tekton.dev/v1, tekton.dev/v1beta1 for StepAction, or v1 for ConfigMap/Secret)", head.APIVersion)
	}
	switch head.Kind {
	case "Task":
		var t tektontypes.Task
		if err := yaml.Unmarshal(data, &t); err != nil {
			return fmt.Errorf("task: %w", err)
		}
		if _, dup := out.Tasks[t.Metadata.Name]; dup {
			return fmt.Errorf("duplicate Task %q", t.Metadata.Name)
		}
		out.Tasks[t.Metadata.Name] = t
	case "Pipeline":
		var p tektontypes.Pipeline
		if err := yaml.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		if _, dup := out.Pipelines[p.Metadata.Name]; dup {
			return fmt.Errorf("duplicate Pipeline %q", p.Metadata.Name)
		}
		out.Pipelines[p.Metadata.Name] = p
	case "PipelineRun":
		var pr tektontypes.PipelineRun
		if err := yaml.Unmarshal(data, &pr); err != nil {
			return fmt.Errorf("PipelineRun: %w", err)
		}
		out.PipelineRuns = append(out.PipelineRuns, pr)
	case "TaskRun":
		var tr tektontypes.TaskRun
		if err := yaml.Unmarshal(data, &tr); err != nil {
			return fmt.Errorf("TaskRun: %w", err)
		}
		out.TaskRuns = append(out.TaskRuns, tr)
	default:
		return fmt.Errorf("unsupported kind %q", head.Kind)
	}
	return nil
}

// loadStepAction parses a `kind: StepAction` (apiVersion tekton.dev/v1beta1)
// doc into the bundle. Duplicate names within a single bundle are an error.
func loadStepAction(out *Bundle, data []byte) error {
	var sa tektontypes.StepAction
	if err := yaml.Unmarshal(data, &sa); err != nil {
		return fmt.Errorf("StepAction: %w", err)
	}
	if sa.Metadata.Name == "" {
		return fmt.Errorf("StepAction: metadata.name is required")
	}
	if _, dup := out.StepActions[sa.Metadata.Name]; dup {
		return fmt.Errorf("duplicate StepAction %q", sa.Metadata.Name)
	}
	if out.StepActions == nil {
		out.StepActions = map[string]tektontypes.StepAction{}
	}
	out.StepActions[sa.Metadata.Name] = sa
	return nil
}

// configMapDoc is the shape we pull out of a `kind: ConfigMap` doc.
// `binaryData` is parsed only so we can reject it explicitly.
// `immutable` is parsed-and-ignored so the same YAML can apply against
// a real cluster.
type configMapDoc struct {
	Metadata   tektontypes.Metadata `json:"metadata"`
	Data       map[string]string    `json:"data,omitempty"`
	BinaryData map[string]string    `json:"binaryData,omitempty"`
	Immutable  *bool                `json:"immutable,omitempty"`
}

func loadConfigMap(out *Bundle, data []byte) error {
	var cm configMapDoc
	if err := yaml.Unmarshal(data, &cm); err != nil {
		return fmt.Errorf("ConfigMap: %w", err)
	}
	if cm.Metadata.Name == "" {
		return fmt.Errorf("ConfigMap: metadata.name is required")
	}
	if len(cm.BinaryData) > 0 {
		return fmt.Errorf("ConfigMap %q: binaryData is not supported (out of scope for tkn-act; use data)", cm.Metadata.Name)
	}
	if _, dup := out.ConfigMaps[cm.Metadata.Name]; dup {
		return fmt.Errorf("duplicate ConfigMap %q", cm.Metadata.Name)
	}
	bytesByKey := make(map[string][]byte, len(cm.Data))
	for k, v := range cm.Data {
		bytesByKey[k] = []byte(v)
	}
	out.ConfigMaps[cm.Metadata.Name] = bytesByKey
	return nil
}

// secretDoc is the shape we pull out of a `kind: Secret` doc.
// `type` is parsed-and-ignored (tkn-act always projects bytes opaquely).
// `immutable` is parsed-and-ignored so the same YAML can apply against
// a real cluster.
type secretDoc struct {
	Metadata   tektontypes.Metadata `json:"metadata"`
	Type       string               `json:"type,omitempty"`
	Data       map[string]string    `json:"data,omitempty"`
	StringData map[string]string    `json:"stringData,omitempty"`
	Immutable  *bool                `json:"immutable,omitempty"`
}

func loadSecret(out *Bundle, data []byte) error {
	var s secretDoc
	if err := yaml.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("Secret: %w", err)
	}
	if s.Metadata.Name == "" {
		return fmt.Errorf("Secret: metadata.name is required")
	}
	if _, dup := out.Secrets[s.Metadata.Name]; dup {
		return fmt.Errorf("duplicate Secret %q", s.Metadata.Name)
	}
	bytesByKey := make(map[string][]byte, len(s.Data)+len(s.StringData))
	for k, v := range s.Data {
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return fmt.Errorf("Secret %q: data[%q] is not valid base64: %w", s.Metadata.Name, k, err)
		}
		bytesByKey[k] = dec
	}
	// stringData wins over data on the same key (kube projection rule).
	for k, v := range s.StringData {
		bytesByKey[k] = []byte(v)
	}
	out.Secrets[s.Metadata.Name] = bytesByKey
	return nil
}

func merge(into, from *Bundle) error {
	for k, v := range from.Tasks {
		if _, dup := into.Tasks[k]; dup {
			return fmt.Errorf("duplicate Task %q across files", k)
		}
		into.Tasks[k] = v
	}
	for k, v := range from.Pipelines {
		if _, dup := into.Pipelines[k]; dup {
			return fmt.Errorf("duplicate Pipeline %q across files", k)
		}
		into.Pipelines[k] = v
	}
	for k, v := range from.StepActions {
		if _, dup := into.StepActions[k]; dup {
			return fmt.Errorf("duplicate StepAction %q across files", k)
		}
		if into.StepActions == nil {
			into.StepActions = map[string]tektontypes.StepAction{}
		}
		into.StepActions[k] = v
	}
	for k, v := range from.ConfigMaps {
		if _, dup := into.ConfigMaps[k]; dup {
			return fmt.Errorf("duplicate ConfigMap %q across files", k)
		}
		into.ConfigMaps[k] = v
	}
	for k, v := range from.Secrets {
		if _, dup := into.Secrets[k]; dup {
			return fmt.Errorf("duplicate Secret %q across files", k)
		}
		into.Secrets[k] = v
	}
	into.PipelineRuns = append(into.PipelineRuns, from.PipelineRuns...)
	into.TaskRuns = append(into.TaskRuns, from.TaskRuns...)
	return nil
}

// UnresolvedRef describes a single resolver-backed taskRef or pipelineRef
// found in a Bundle. PipelineTask names the PipelineTask that carries the
// resolver block; for the top-level pipelineRef on a PipelineRun, it's "".
// Pipeline names the enclosing Pipeline (or empty for the top-level
// PipelineRun.spec.pipelineRef case). Kind is "Task" or "Pipeline".
type UnresolvedRef struct {
	Pipeline     string
	PipelineTask string
	Kind         string
	Resolver     string
}

// HasUnresolvedRefs lists every taskRef.resolver / pipelineRef.resolver
// found in the Bundle. Used by validate -o json and --offline pre-flight
// to tell users exactly which references would need to resolve.
func HasUnresolvedRefs(b *Bundle) []UnresolvedRef {
	if b == nil {
		return nil
	}
	var out []UnresolvedRef
	// Per-Pipeline taskRef resolvers (main DAG + finally).
	for plName, pl := range b.Pipelines {
		for _, pt := range pl.Spec.Tasks {
			if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
				out = append(out, UnresolvedRef{
					Pipeline: plName, PipelineTask: pt.Name, Kind: "Task",
					Resolver: pt.TaskRef.Resolver,
				})
			}
		}
		for _, pt := range pl.Spec.Finally {
			if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
				out = append(out, UnresolvedRef{
					Pipeline: plName, PipelineTask: pt.Name, Kind: "Task",
					Resolver: pt.TaskRef.Resolver,
				})
			}
		}
	}
	// Top-level PipelineRun.spec.pipelineRef resolvers.
	for _, pr := range b.PipelineRuns {
		if pr.Spec.PipelineRef != nil && pr.Spec.PipelineRef.Resolver != "" {
			out = append(out, UnresolvedRef{
				Pipeline: "", PipelineTask: "", Kind: "Pipeline",
				Resolver: pr.Spec.PipelineRef.Resolver,
			})
		}
	}
	// Top-level TaskRun.spec.taskRef resolvers (rare but possible).
	for _, tr := range b.TaskRuns {
		if tr.Spec.TaskRef != nil && tr.Spec.TaskRef.Resolver != "" {
			out = append(out, UnresolvedRef{
				Pipeline: "", PipelineTask: "", Kind: "Task",
				Resolver: tr.Spec.TaskRef.Resolver,
			})
		}
	}
	return out
}

// docSep matches a YAML document separator on its own line: `---` (optionally
// followed by whitespace), allowing for an optional leading BOM/whitespace.
var docSep = regexp.MustCompile(`(?m)^---\s*$`)

// splitYAMLDocs splits a multi-doc YAML stream by `---`. Empty docs are skipped.
func splitYAMLDocs(data []byte) ([][]byte, error) {
	parts := docSep.Split(string(data), -1)
	var out [][]byte
	for _, p := range parts {
		b := []byte(p)
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}
