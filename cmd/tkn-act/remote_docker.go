package main

import (
	"fmt"
	"os"
)

// envRemoteDocker is the environment variable that overrides
// auto-detection of remote-docker mode for the --docker backend.
// One of: "auto", "on", "off". Precedence: --remote-docker flag >
// $TKN_ACT_REMOTE_DOCKER > "auto".
const envRemoteDocker = "TKN_ACT_REMOTE_DOCKER"

// resolveRemoteDocker normalizes the --remote-docker flag value
// against $TKN_ACT_REMOTE_DOCKER. Returns "auto", "on", or "off",
// or an error when the flag itself is unrecognized — typos like
// `--remote-docker=onn` should not silently revert to auto.
//
// Only "on" and "off" are decisive at each layer; "auto" (the flag
// default) is a pass-through, so an env preference survives the
// unflagged invocation. Unknown values *in the env* are tolerated
// silently because envs can be inherited from outside the user's
// intent — only the explicit flag is strict.
func resolveRemoteDocker(flagValue string) (string, error) {
	switch flagValue {
	case "on", "off":
		return flagValue, nil
	case "", "auto":
		// fall through
	default:
		return "", fmt.Errorf("invalid --remote-docker=%q (want auto|on|off)", flagValue)
	}
	if v := decisiveRemoteDockerValue(os.Getenv(envRemoteDocker)); v != "" {
		return v, nil
	}
	return "auto", nil
}

func decisiveRemoteDockerValue(v string) string {
	switch v {
	case "on", "off":
		return v
	}
	return ""
}
