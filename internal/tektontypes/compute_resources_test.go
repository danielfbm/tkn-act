package tektontypes

import (
	"encoding/json"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestStepComputeResources: Tekton v1 names the field
// `computeResources`; tkn-act used to read only the v1beta1
// `resources`, silently dropping v1 limits on both backends.
func TestStepComputeResources(t *testing.T) {
	in := []byte(`
steps:
  - name: v1
    image: alpine
    computeResources:
      limits: {cpu: 1, memory: 64Mi, nvidia.com/gpu: "1"}
      requests: {ephemeral-storage: 1Gi}
  - name: legacy
    image: alpine
    resources:
      limits: {memory: 32Mi}
  - name: both
    image: alpine
    resources: {limits: {memory: 1Mi}}
    computeResources: {limits: {memory: 2Mi}}
stepTemplate:
  resources: {limits: {cpu: 250m}}
sidecars:
  - name: sc
    image: redis
    computeResources: {limits: {memory: 8Mi}}
`)
	var ts TaskSpec
	if err := yaml.Unmarshal(in, &ts); err != nil {
		t.Fatal(err)
	}
	v1 := ts.Steps[0].Resources
	if v1 == nil || v1.Limits.CPU != "1" || v1.Limits.Memory != "64Mi" || v1.Limits.Other["nvidia.com/gpu"] != "1" || v1.Requests.Other["ephemeral-storage"] != "1Gi" {
		t.Errorf("computeResources = %+v", v1)
	}
	if r := ts.Steps[1].Resources; r == nil || r.Limits.Memory != "32Mi" {
		t.Errorf("resources alias = %+v", r)
	}
	if r := ts.Steps[2].Resources; r == nil || r.Limits.Memory != "2Mi" {
		t.Errorf("computeResources must win over resources: %+v", r)
	}
	if r := ts.StepTemplate.Resources; r == nil || r.Limits.CPU != "250m" {
		t.Errorf("stepTemplate resources alias = %+v", r)
	}
	if r := ts.Sidecars[0].Resources; r == nil || r.Limits.Memory != "8Mi" {
		t.Errorf("sidecar computeResources = %+v", r)
	}

	out, err := json.Marshal(ts.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"computeResources"`) || strings.Contains(s, `"resources"`) {
		t.Errorf("re-marshal must use computeResources: %s", s)
	}
	if !strings.Contains(s, `"nvidia.com/gpu":"1"`) || !strings.Contains(s, `"ephemeral-storage":"1Gi"`) {
		t.Errorf("extra resource names lost: %s", s)
	}
}

func TestResourceListRejectsNonScalar(t *testing.T) {
	var r ResourceList
	if err := json.Unmarshal([]byte(`{"cpu": {"x": 1}}`), &r); err == nil {
		t.Error("want error for object quantity")
	}
}

// TestStepFieldsRoundTrip: securityContext, envFrom, env valueFrom and
// step timeout used to be dropped by the non-strict parse, so the
// cluster backend submitted a weaker spec than written.
func TestStepFieldsRoundTrip(t *testing.T) {
	in := []byte(`
name: s
image: alpine
timeout: 30s
securityContext:
  runAsUser: 65532
  readOnlyRootFilesystem: true
  capabilities: {drop: [ALL]}
envFrom:
  - configMapRef: {name: cfg}
env:
  - name: TOKEN
    valueFrom:
      secretKeyRef: {name: creds, key: token}
  - name: PLAIN
    value: x
`)
	var st Step
	if err := yaml.Unmarshal(in, &st); err != nil {
		t.Fatal(err)
	}
	if st.Timeout != "30s" || st.SecurityContext == nil || *st.SecurityContext.RunAsUser != 65532 ||
		len(st.EnvFrom) != 1 || st.Env[0].ValueFrom == nil || st.Env[0].ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("parsed step = %+v", st)
	}
	out, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"timeout":"30s"`, `"runAsUser":65532`, `"readOnlyRootFilesystem":true`, `"configMapRef":{"name":"cfg"}`, `"secretKeyRef":{"name":"creds","key":"token"}`, `"value":"x"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("marshal missing %s: %s", want, out)
		}
	}
	if strings.Contains(string(out), `"name":"TOKEN","value"`) {
		t.Errorf("valueFrom env must not carry an empty value: %s", out)
	}
}
