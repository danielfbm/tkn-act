// Package tektontypes defines minimal Go types matching the tekton.dev/v1 schema
// for Task, TaskRun, Pipeline, and PipelineRun. JSON tags align with upstream so
// `kubectl apply -f` and `tkn-act` parse the same YAML identically.
//
// Scope is intentionally narrow — only fields that the v1 implementation actually
// reads. Fields we don't support (sidecars, stepActions, retries, resolvers) are
// not parsed.
package tektontypes

import (
	"encoding/json"
	"fmt"
)

// Object is the common envelope shared by Task/Pipeline/TaskRun/PipelineRun.
type Object struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
}

type Metadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ---- Task ----

type Task struct {
	Object `json:",inline"`
	Spec   TaskSpec `json:"spec"`
}

type TaskSpec struct {
	Params      []ParamSpec     `json:"params,omitempty"`
	Results     []ResultSpec    `json:"results,omitempty"`
	Workspaces  []WorkspaceDecl `json:"workspaces,omitempty"`
	Steps       []Step          `json:"steps"`
	DisplayName string          `json:"displayName,omitempty"`
	Description string          `json:"description,omitempty"`
	// Timeout is a Go duration string (e.g. "30s", "5m"). Empty means no
	// task-level timeout.
	Timeout      string        `json:"timeout,omitempty"`
	Volumes      []Volume      `json:"volumes,omitempty"`
	StepTemplate *StepTemplate `json:"stepTemplate,omitempty"`
}

// StepTemplate is the partial-Step template merged into every Step in
// TaskSpec.Steps. Fields are inherited only when the Step doesn't set
// its own. Mirrors Tekton's StepTemplate (v1) for the subset of Step
// fields tkn-act reads. `name`, `script`, `volumeMounts`, `results`,
// and `onError` are NOT inheritable — they're intrinsically per-Step.
type StepTemplate struct {
	Image           string         `json:"image,omitempty"`
	Command         []string       `json:"command,omitempty"`
	Args            []string       `json:"args,omitempty"`
	Env             []EnvVar       `json:"env,omitempty"`
	WorkingDir      string         `json:"workingDir,omitempty"`
	Resources       *StepResources `json:"resources,omitempty"`
	ImagePullPolicy string         `json:"imagePullPolicy,omitempty"`
}

type Step struct {
	Name            string         `json:"name"`
	DisplayName     string         `json:"displayName,omitempty"`
	Description     string         `json:"description,omitempty"`
	Image           string         `json:"image"`
	Command         []string       `json:"command,omitempty"`
	Args            []string       `json:"args,omitempty"`
	Script          string         `json:"script,omitempty"`
	Env             []EnvVar       `json:"env,omitempty"`
	WorkingDir      string         `json:"workingDir,omitempty"`
	Resources       *StepResources `json:"resources,omitempty"`
	ImagePullPolicy string         `json:"imagePullPolicy,omitempty"` // Always | IfNotPresent | Never
	// OnError controls Task-level failure semantics. "" or "stopAndFail" is
	// the default — first non-zero step exit fails the Task. "continue" lets
	// a non-zero exit be recorded but does not fail the Task.
	OnError string `json:"onError,omitempty"`
	// Results are per-step results, mounted at /tekton/steps/<step>/results/
	// in this step (RW) and in every later step in the same Task (RO).
	Results      []ResultSpec  `json:"results,omitempty"`
	VolumeMounts []VolumeMount `json:"volumeMounts,omitempty"`
}

// Volume is a Task-level volume. Exactly one of EmptyDir/HostPath/ConfigMap/
// Secret must be set; any other source kind is rejected by the validator.
type Volume struct {
	Name      string           `json:"name"`
	EmptyDir  *EmptyDirSource  `json:"emptyDir,omitempty"`
	HostPath  *HostPathSource  `json:"hostPath,omitempty"`
	ConfigMap *ConfigMapSource `json:"configMap,omitempty"`
	Secret    *SecretSource    `json:"secret,omitempty"`
}

type EmptyDirSource struct {
	// Medium: "" (disk-backed tmpdir) or "Memory" (tmpfs on Linux).
	Medium string `json:"medium,omitempty"`
}

type HostPathSource struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"` // diagnostic only
}

type ConfigMapSource struct {
	Name     string      `json:"name"`
	Items    []KeyToPath `json:"items,omitempty"`
	Optional *bool       `json:"optional,omitempty"`
}

type SecretSource struct {
	SecretName string      `json:"secretName"`
	Items      []KeyToPath `json:"items,omitempty"`
	Optional   *bool       `json:"optional,omitempty"`
}

type KeyToPath struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
	SubPath   string `json:"subPath,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type StepResources struct {
	Limits   ResourceList `json:"limits,omitempty"`
	Requests ResourceList `json:"requests,omitempty"`
}

type ResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type ParamSpec struct {
	Name        string      `json:"name"`
	Type        ParamType   `json:"type,omitempty"` // default string
	Description string      `json:"description,omitempty"`
	Default     *ParamValue `json:"default,omitempty"`
}

type ResultSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"` // string|array|object; default string
}

type WorkspaceDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MountPath   string `json:"mountPath,omitempty"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}

// ---- Pipeline ----

type Pipeline struct {
	Object `json:",inline"`
	Spec   PipelineSpec `json:"spec"`
}

type PipelineSpec struct {
	DisplayName string                  `json:"displayName,omitempty"`
	Description string                  `json:"description,omitempty"`
	Params      []ParamSpec             `json:"params,omitempty"`
	Workspaces  []PipelineWorkspaceDecl `json:"workspaces,omitempty"`
	Tasks       []PipelineTask          `json:"tasks"`
	Finally     []PipelineTask          `json:"finally,omitempty"`
	Results     []PipelineResultSpec    `json:"results,omitempty"`
	Timeouts    *Timeouts               `json:"timeouts,omitempty"`
}

// Timeouts mirrors Tekton's PipelineSpec.Timeouts (tekton.dev/v1).
//
// Each field is a Go-style time.Duration string (e.g. "30s", "5m", "2h").
// Unset fields mean "no budget at this level". Validator enforces:
// durations parseable, none equals zero, and tasks+finally ≤ pipeline
// when all three are set.
type Timeouts struct {
	Pipeline string `json:"pipeline,omitempty"`
	Tasks    string `json:"tasks,omitempty"`
	Finally  string `json:"finally,omitempty"`
}

type PipelineWorkspaceDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}

type PipelineResultSpec struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Value       ParamValue `json:"value"`
}

type PipelineTask struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName,omitempty"`
	TaskRef     *TaskRef           `json:"taskRef,omitempty"`
	TaskSpec    *TaskSpec          `json:"taskSpec,omitempty"` // inline task
	Params      []Param            `json:"params,omitempty"`
	Workspaces  []WorkspaceBinding `json:"workspaces,omitempty"`
	RunAfter    []string           `json:"runAfter,omitempty"`
	When        []WhenExpression   `json:"when,omitempty"`
	// Retries is the number of additional attempts after the first failure.
	// 0 (or unset) means run once.
	Retries int `json:"retries,omitempty"`
}

type TaskRef struct {
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"` // Task|ClusterTask; default Task
	// Resolver names a Tekton resolver (git | hub | http | bundles |
	// cluster | custom-name-in-remote-mode). When non-empty, Name is
	// ignored — the resolver is authoritative.
	Resolver string `json:"resolver,omitempty"`
	// ResolverParams are the resolver-specific name=value pairs nested
	// inside the `resolver:` block. The YAML key is "params" because
	// Tekton's schema places this list inside `taskRef:`; this is a
	// distinct nesting from PipelineTask.Params.
	ResolverParams []ResolverParam `json:"params,omitempty"`
}

// ResolverParam is the substitution-eligible shape resolvers consume.
// Mirrors Tekton's tekton.dev/v1 resolver param: name + value.
type ResolverParam struct {
	Name  string     `json:"name"`
	Value ParamValue `json:"value"`
}

type WorkspaceBinding struct {
	Name      string `json:"name"`      // workspace name as declared in the Task
	Workspace string `json:"workspace"` // pipeline workspace it binds to
	SubPath   string `json:"subPath,omitempty"`
}

type WhenExpression struct {
	Input    string   `json:"input"`
	Operator string   `json:"operator"` // "in" | "notin"
	Values   []string `json:"values"`
}

// ---- PipelineRun & TaskRun (sparse — we synthesize most of these) ----

type PipelineRun struct {
	Object `json:",inline"`
	Spec   PipelineRunSpec `json:"spec"`
}

type PipelineRunSpec struct {
	PipelineRef  *PipelineRef           `json:"pipelineRef,omitempty"`
	PipelineSpec *PipelineSpec          `json:"pipelineSpec,omitempty"`
	Params       []Param                `json:"params,omitempty"`
	Workspaces   []PipelineRunWSBinding `json:"workspaces,omitempty"`
}

type PipelineRef struct {
	Name string `json:"name,omitempty"`
	// Resolver names a Tekton resolver (git | hub | http | bundles |
	// cluster | custom-name-in-remote-mode). When non-empty, Name is
	// ignored — the resolver is authoritative.
	Resolver string `json:"resolver,omitempty"`
	// ResolverParams are the resolver-specific name=value pairs nested
	// inside the `resolver:` block of a pipelineRef.
	ResolverParams []ResolverParam `json:"params,omitempty"`
}

type PipelineRunWSBinding struct {
	Name     string    `json:"name"`
	EmptyDir *struct{} `json:"emptyDir,omitempty"`
	HostPath string    `json:"-"` // tkn-act extension; populated from CLI -w flag
}

type TaskRun struct {
	Object `json:",inline"`
	Spec   TaskRunSpec `json:"spec"`
}

type TaskRunSpec struct {
	TaskRef    *TaskRef           `json:"taskRef,omitempty"`
	TaskSpec   *TaskSpec          `json:"taskSpec,omitempty"`
	Params     []Param            `json:"params,omitempty"`
	Workspaces []WorkspaceBinding `json:"workspaces,omitempty"`
}

// ---- Param value (scalar | array | object) ----

type Param struct {
	Name  string     `json:"name"`
	Value ParamValue `json:"value"`
}

type ParamType string

const (
	ParamTypeString ParamType = "string"
	ParamTypeArray  ParamType = "array"
	ParamTypeObject ParamType = "object"
)

// ParamValue can be a string, []string, or map[string]string. Custom JSON
// unmarshaler picks the right shape from the input.
type ParamValue struct {
	Type      ParamType
	StringVal string
	ArrayVal  []string
	ObjectVal map[string]string
}

func (v *ParamValue) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	switch data[0] {
	case '"':
		v.Type = ParamTypeString
		return json.Unmarshal(data, &v.StringVal)
	case '[':
		v.Type = ParamTypeArray
		return json.Unmarshal(data, &v.ArrayVal)
	case '{':
		v.Type = ParamTypeObject
		return json.Unmarshal(data, &v.ObjectVal)
	default:
		// numbers / bools / etc. — coerce to string per Tekton convention
		v.Type = ParamTypeString
		v.StringVal = string(data)
		return nil
	}
}

func (v ParamValue) MarshalJSON() ([]byte, error) {
	switch v.Type {
	case ParamTypeArray:
		return json.Marshal(v.ArrayVal)
	case ParamTypeObject:
		return json.Marshal(v.ObjectVal)
	case ParamTypeString, "":
		return json.Marshal(v.StringVal)
	default:
		return nil, fmt.Errorf("unknown param type %q", v.Type)
	}
}
