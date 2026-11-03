package docker

import (
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/container"
	corev1 "k8s.io/api/core/v1"
)

func ptr[T any](v T) *T { return &v }

func TestApplySecurityContext(t *testing.T) {
	sc := &corev1.SecurityContext{
		RunAsUser:                ptr(int64(65532)),
		RunAsGroup:               ptr(int64(1000)),
		Privileged:               ptr(true),
		ReadOnlyRootFilesystem:   ptr(true),
		AllowPrivilegeEscalation: ptr(false),
		Capabilities:             &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}, Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
	}
	cfg := &container.Config{}
	hc := &container.HostConfig{}
	if err := applySecurityContext(sc, cfg, hc); err != nil {
		t.Fatal(err)
	}
	if cfg.User != "65532:1000" || !hc.Privileged || !hc.ReadonlyRootfs {
		t.Errorf("cfg=%+v hc privileged=%v ro=%v", cfg.User, hc.Privileged, hc.ReadonlyRootfs)
	}
	if strings.Join(hc.CapAdd, ",") != "NET_ADMIN" || strings.Join(hc.CapDrop, ",") != "ALL" {
		t.Errorf("caps add=%v drop=%v", hc.CapAdd, hc.CapDrop)
	}
	if strings.Join(hc.SecurityOpt, ",") != "seccomp=unconfined,no-new-privileges" {
		t.Errorf("securityOpt = %v", hc.SecurityOpt)
	}
	if err := applySecurityContext(nil, cfg, hc); err != nil {
		t.Error(err)
	}
}

func TestApplySecurityContext_Unsupported(t *testing.T) {
	for name, sc := range map[string]*corev1.SecurityContext{
		"seLinuxOptions": {SELinuxOptions: &corev1.SELinuxOptions{Level: "s0"}},
		"seccompProfile": {SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeLocalhost}},
		"runAsGroup":     {RunAsGroup: ptr(int64(1))},
		"runAsNonRoot":   {RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(0))},
		"windowsOptions": {WindowsOptions: &corev1.WindowsSecurityContextOptions{}},
	} {
		if err := applySecurityContext(sc, &container.Config{}, &container.HostConfig{}); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCheckRunAsNonRoot(t *testing.T) {
	for user, ok := range map[string]bool{"": false, "root": false, "0": false, "0:0": false, "65532": true, "65532:65532": true, "nobody": false} {
		err := checkRunAsNonRoot("img", user)
		if (err == nil) != ok {
			t.Errorf("user %q: err = %v", user, err)
		}
	}
	if !needsImageUserCheck(&corev1.SecurityContext{RunAsNonRoot: ptr(true)}) ||
		needsImageUserCheck(&corev1.SecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(5))}) ||
		needsImageUserCheck(nil) {
		t.Error("needsImageUserCheck")
	}
}

// TestCheckDockerSupport: envFrom / valueFrom / bad timeouts must fail
// the Task on docker instead of being silently dropped.
func TestCheckDockerSupport(t *testing.T) {
	ok := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Timeout: "5s", Env: []tektontypes.EnvVar{{Name: "A", Value: "b"}}}}}
	if err := checkDockerSupport(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]tektontypes.TaskSpec{
		"envFrom":        {Steps: []tektontypes.Step{{Name: "s", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{}}}}}},
		"valueFrom":      {Steps: []tektontypes.Step{{Name: "s", Env: []tektontypes.EnvVar{{Name: "A", ValueFrom: &corev1.EnvVarSource{}}}}}},
		"timeout":        {Steps: []tektontypes.Step{{Name: "s", Timeout: "soon"}}},
		"sidecar":        {Sidecars: []tektontypes.Sidecar{{Name: "sc", EnvFrom: []corev1.EnvFromSource{{}}}}},
		"seLinuxOptions": {Steps: []tektontypes.Step{{Name: "s", SecurityContext: &corev1.SecurityContext{SELinuxOptions: &corev1.SELinuxOptions{}}}}},
	}
	for name, spec := range cases {
		err := checkDockerSupport(spec)
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		if name != "sidecar" && !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := parseStepTimeout("-1s"); err == nil {
		t.Error("negative timeout accepted")
	}
}
