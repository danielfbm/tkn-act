// Package loader parses one or more Tekton YAML files into a Bundle of typed
// resources keyed by name. Multi-document YAML is supported.
package loader

import (
	"bytes"
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
		Tasks:      map[string]tektontypes.Task{},
		Pipelines:  map[string]tektontypes.Pipeline{},
		ConfigMaps: map[string]map[string][]byte{},
		Secrets:    map[string]map[string][]byte{},
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
		Tasks:      map[string]tektontypes.Task{},
		Pipelines:  map[string]tektontypes.Pipeline{},
		ConfigMaps: map[string]map[string][]byte{},
		Secrets:    map[string]map[string][]byte{},
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
		// fall through to the Tekton-kind switch below
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
		return fmt.Errorf("unsupported apiVersion %q (only tekton.dev/v1, or v1 for ConfigMap/Secret)", head.APIVersion)
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

func loadSecret(out *Bundle, data []byte) error {
	return fmt.Errorf("Secret loading not yet implemented (Task 2)")
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
