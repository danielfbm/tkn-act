package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/container"
	corev1 "k8s.io/api/core/v1"
)

// checkDockerSupport rejects Task fields the docker backend cannot
// honour, before any container starts. Silently dropping them would
// run a weaker (or differently configured) container than the YAML
// asks for; the cluster backend passes all of them through to Tekton.
func checkDockerSupport(spec tektontypes.TaskSpec) error {
	for _, st := range spec.Steps {
		if err := checkContainerSupport(fmt.Sprintf("step %q", st.Name), st.Env, st.EnvFrom, st.SecurityContext); err != nil {
			return err
		}
		if st.Timeout != "" {
			if _, err := parseStepTimeout(st.Timeout); err != nil {
				return fmt.Errorf("step %q: %w", st.Name, err)
			}
		}
	}
	for _, sc := range spec.Sidecars {
		if err := checkContainerSupport(fmt.Sprintf("sidecar %q", sc.Name), sc.Env, sc.EnvFrom, sc.SecurityContext); err != nil {
			return err
		}
	}
	return nil
}

func checkContainerSupport(owner string, env []tektontypes.EnvVar, envFrom []corev1.EnvFromSource, sc *corev1.SecurityContext) error {
	if len(envFrom) > 0 {
		return fmt.Errorf("%s: envFrom is not supported on the docker backend (use --cluster, or inline the values with env[].value)", owner)
	}
	for _, e := range env {
		if e.ValueFrom != nil {
			return fmt.Errorf("%s: env %q uses valueFrom, which is not supported on the docker backend (use --cluster, or set env[].value)", owner, e.Name)
		}
	}
	if err := applySecurityContext(sc, &container.Config{}, &container.HostConfig{}); err != nil {
		return fmt.Errorf("%s: %w", owner, err)
	}
	return nil
}

// applySecurityContext maps the subset of a Kubernetes SecurityContext
// that has a faithful Docker equivalent onto the container config, and
// returns an error naming any sub-field it cannot honour.
// runAsNonRoot without runAsUser is checked separately against the
// image's configured user (see checkRunAsNonRoot).
func applySecurityContext(sc *corev1.SecurityContext, cfg *container.Config, hc *container.HostConfig) error {
	if sc == nil {
		return nil
	}
	var unsupported []string
	if sc.SELinuxOptions != nil {
		unsupported = append(unsupported, "seLinuxOptions")
	}
	if sc.WindowsOptions != nil {
		unsupported = append(unsupported, "windowsOptions")
	}
	if sc.ProcMount != nil && *sc.ProcMount != corev1.DefaultProcMount {
		unsupported = append(unsupported, "procMount")
	}
	if sc.AppArmorProfile != nil && sc.AppArmorProfile.Type != corev1.AppArmorProfileTypeRuntimeDefault {
		unsupported = append(unsupported, "appArmorProfile")
	}
	if sp := sc.SeccompProfile; sp != nil {
		switch sp.Type {
		case corev1.SeccompProfileTypeRuntimeDefault:
		case corev1.SeccompProfileTypeUnconfined:
			hc.SecurityOpt = append(hc.SecurityOpt, "seccomp=unconfined")
		default:
			unsupported = append(unsupported, "seccompProfile.type="+string(sp.Type))
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("securityContext %s not supported on the docker backend (use --cluster)", strings.Join(unsupported, ", "))
	}

	if sc.RunAsUser != nil {
		cfg.User = strconv.FormatInt(*sc.RunAsUser, 10)
		if sc.RunAsGroup != nil {
			cfg.User += ":" + strconv.FormatInt(*sc.RunAsGroup, 10)
		}
	} else if sc.RunAsGroup != nil {
		return fmt.Errorf("securityContext.runAsGroup without runAsUser is not supported on the docker backend")
	}
	if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot && sc.RunAsUser != nil && *sc.RunAsUser == 0 {
		return fmt.Errorf("securityContext: runAsNonRoot is true but runAsUser is 0")
	}
	if sc.Privileged != nil && *sc.Privileged {
		hc.Privileged = true
	}
	if sc.ReadOnlyRootFilesystem != nil && *sc.ReadOnlyRootFilesystem {
		hc.ReadonlyRootfs = true
	}
	if sc.AllowPrivilegeEscalation != nil && !*sc.AllowPrivilegeEscalation {
		hc.SecurityOpt = append(hc.SecurityOpt, "no-new-privileges")
	}
	if c := sc.Capabilities; c != nil {
		for _, cp := range c.Add {
			hc.CapAdd = append(hc.CapAdd, string(cp))
		}
		for _, cp := range c.Drop {
			hc.CapDrop = append(hc.CapDrop, string(cp))
		}
	}
	return nil
}

// needsImageUserCheck reports whether runAsNonRoot must be verified
// against the image's configured USER (no explicit runAsUser).
func needsImageUserCheck(sc *corev1.SecurityContext) bool {
	return sc != nil && sc.RunAsNonRoot != nil && *sc.RunAsNonRoot && sc.RunAsUser == nil
}

// checkRunAsNonRoot mirrors the kubelet check: with runAsNonRoot and
// no runAsUser, the image's USER must be a non-zero numeric UID. A
// named non-root user can't be verified without the image's passwd
// file, so (like the kubelet) it is refused too.
func checkRunAsNonRoot(image, imageUser string) error {
	u := imageUser
	if i := strings.IndexByte(u, ':'); i >= 0 {
		u = u[:i]
	}
	if u == "" || u == "root" {
		return fmt.Errorf("securityContext.runAsNonRoot: image %q will run as root", image)
	}
	uid, err := strconv.ParseInt(u, 10, 64)
	if err != nil {
		return fmt.Errorf("securityContext.runAsNonRoot: image %q has non-numeric user %q, cannot verify it is non-root (set runAsUser)", image, imageUser)
	}
	if uid == 0 {
		return fmt.Errorf("securityContext.runAsNonRoot: image %q will run as root", image)
	}
	return nil
}

// stepTimeoutExitCode is the exit code recorded for a Step stopped by
// its Step.timeout (Tekton's entrypoint also exits 1 on timeout).
const stepTimeoutExitCode = 1

// checkImageNonRoot inspects image's configured USER for the
// runAsNonRoot-without-runAsUser case.
func (b *Backend) checkImageNonRoot(ctx context.Context, image string) error {
	insp, _, err := b.cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		return fmt.Errorf("inspect %s for runAsNonRoot: %w", image, err)
	}
	user := ""
	if insp.Config != nil {
		user = insp.Config.User
	}
	return checkRunAsNonRoot(image, user)
}

// parseStepTimeout parses Step.timeout. Zero means no timeout.
func parseStepTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid timeout %q: must not be negative", s)
	}
	return d, nil
}
