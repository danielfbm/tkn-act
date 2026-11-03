package engine

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
	corev1 "k8s.io/api/core/v1"
)

func TestApplyStepTemplateInheritsSecurityContextAndEnvFrom(t *testing.T) {
	uid := int64(65532)
	other := int64(1)
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			SecurityContext: &corev1.SecurityContext{RunAsUser: &uid},
			EnvFrom:         []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}},
		},
		Steps: []tektontypes.Step{
			{Name: "inherit"},
			{Name: "own", SecurityContext: &corev1.SecurityContext{RunAsUser: &other}},
		},
	}
	out := applyStepTemplate(spec)
	if sc := out.Steps[0].SecurityContext; sc == nil || *sc.RunAsUser != 65532 {
		t.Errorf("inherit securityContext = %+v", sc)
	}
	if len(out.Steps[0].EnvFrom) != 1 {
		t.Errorf("inherit envFrom = %+v", out.Steps[0].EnvFrom)
	}
	if *out.Steps[1].SecurityContext.RunAsUser != 1 {
		t.Error("step securityContext must win")
	}
	// Deep copy: mutating the merged step must not touch the template.
	*out.Steps[0].SecurityContext.RunAsUser = 7
	if *spec.StepTemplate.SecurityContext.RunAsUser != 65532 {
		t.Error("template securityContext aliased into step")
	}
}
