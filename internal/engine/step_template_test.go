package engine

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestApplyStepTemplateNil(t *testing.T) {
	spec := tektontypes.TaskSpec{
		Steps: []tektontypes.Step{{Name: "a", Image: "alpine:3"}},
	}
	got := applyStepTemplate(spec)
	if !reflect.DeepEqual(got, spec) {
		t.Errorf("nil template should be a no-op, got %+v", got)
	}
}

func TestApplyStepTemplateInheritsImage(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{Image: "alpine:3"},
		Steps: []tektontypes.Step{
			{Name: "a"},                   // no image -> inherits
			{Name: "b", Image: "busybox"}, // overrides
		},
	}
	got := applyStepTemplate(spec)
	if got.Steps[0].Image != "alpine:3" {
		t.Errorf("step a image = %q, want alpine:3 (inherited)", got.Steps[0].Image)
	}
	if got.Steps[1].Image != "busybox" {
		t.Errorf("step b image = %q, want busybox (override)", got.Steps[1].Image)
	}
}

func TestApplyStepTemplateInheritsScalars(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Image:           "alpine:3",
			WorkingDir:      "/work",
			ImagePullPolicy: "IfNotPresent",
		},
		Steps: []tektontypes.Step{{Name: "a"}},
	}
	got := applyStepTemplate(spec).Steps[0]
	if got.Image != "alpine:3" || got.WorkingDir != "/work" || got.ImagePullPolicy != "IfNotPresent" {
		t.Errorf("step = %+v, want all three inherited", got)
	}
}

func TestApplyStepTemplateInheritsCommandArgsWhenStepEmpty(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Command: []string{"/bin/sh", "-c"},
			Args:    []string{"echo hi"},
		},
		Steps: []tektontypes.Step{
			{Name: "a"},
			{Name: "b", Command: []string{"/bin/bash"}},
			{Name: "c", Args: []string{"echo bye"}},
		},
	}
	got := applyStepTemplate(spec).Steps
	if !reflect.DeepEqual(got[0].Command, []string{"/bin/sh", "-c"}) || !reflect.DeepEqual(got[0].Args, []string{"echo hi"}) {
		t.Errorf("a: command/args = %+v / %+v", got[0].Command, got[0].Args)
	}
	if !reflect.DeepEqual(got[1].Command, []string{"/bin/bash"}) {
		t.Errorf("b command = %+v, want override", got[1].Command)
	}
	if !reflect.DeepEqual(got[1].Args, []string{"echo hi"}) {
		t.Errorf("b args = %+v, want inherited (only Command was set)", got[1].Args)
	}
	if !reflect.DeepEqual(got[2].Args, []string{"echo bye"}) {
		t.Errorf("c args = %+v, want override", got[2].Args)
	}
}

func TestApplyStepTemplateMergesEnvByName(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Env: []tektontypes.EnvVar{
				{Name: "A", Value: "from-template"},
				{Name: "B", Value: "from-template"},
			},
		},
		Steps: []tektontypes.Step{
			{
				Name: "a",
				Env: []tektontypes.EnvVar{
					{Name: "B", Value: "from-step"}, // wins by name
					{Name: "C", Value: "step-only"},
				},
			},
		},
	}
	got := applyStepTemplate(spec).Steps[0].Env
	want := []tektontypes.EnvVar{
		{Name: "A", Value: "from-template"},
		{Name: "B", Value: "from-step"},
		{Name: "C", Value: "step-only"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %+v, want %+v", got, want)
	}
}

func TestApplyStepTemplateInheritsResources(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Resources: &tektontypes.StepResources{
				Limits: tektontypes.ResourceList{Memory: "128Mi"},
			},
		},
		Steps: []tektontypes.Step{
			{Name: "a"},
			{Name: "b", Resources: &tektontypes.StepResources{Limits: tektontypes.ResourceList{Memory: "256Mi"}}},
		},
	}
	got := applyStepTemplate(spec).Steps
	if got[0].Resources == nil || got[0].Resources.Limits.Memory != "128Mi" {
		t.Errorf("a resources = %+v, want inherited 128Mi", got[0].Resources)
	}
	if got[1].Resources.Limits.Memory != "256Mi" {
		t.Errorf("b resources = %+v, want override 256Mi", got[1].Resources)
	}
}

func TestApplyStepTemplateDoesNotMutateInput(t *testing.T) {
	tmpl := &tektontypes.StepTemplate{Env: []tektontypes.EnvVar{{Name: "A", Value: "1"}}}
	spec := tektontypes.TaskSpec{
		StepTemplate: tmpl,
		Steps:        []tektontypes.Step{{Name: "a", Env: []tektontypes.EnvVar{{Name: "B", Value: "2"}}}},
	}
	_ = applyStepTemplate(spec)
	if len(tmpl.Env) != 1 || tmpl.Env[0].Name != "A" {
		t.Errorf("template env mutated: %+v", tmpl.Env)
	}
	if len(spec.Steps[0].Env) != 1 || spec.Steps[0].Env[0].Name != "B" {
		t.Errorf("step env mutated: %+v", spec.Steps[0].Env)
	}
}
