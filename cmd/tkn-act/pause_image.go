package main

import "os"

// envPauseImage is the environment variable that overrides the
// docker backend's pause/stager image. Precedence (highest first):
//
//	--pause-image flag > $TKN_ACT_PAUSE_IMAGE > built-in default
//
// Air-gap users typically set this once per host to point both the
// per-Task netns owner and (Phase 3 onwards) the volume stager at an
// internal-mirror tag.
const envPauseImage = "TKN_ACT_PAUSE_IMAGE"

// resolvePauseImage normalizes the --pause-image flag value against
// $TKN_ACT_PAUSE_IMAGE. Returns the resolved image (possibly empty,
// which the docker backend treats as "use the built-in default").
//
// Symmetry with resolveRemoteDocker: an explicit non-empty flag wins;
// otherwise an env value is honored as-is. Env-supplied values are
// not validated here — image-tag validity surfaces at first pull, and
// silently ignoring an env typo would be more confusing than letting
// the daemon report it.
func resolvePauseImage(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(envPauseImage)
}
