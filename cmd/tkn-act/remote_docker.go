package main

import "os"

// envRemoteDocker is the environment variable that overrides
// auto-detection of remote-docker mode for the --docker backend.
// One of: "auto", "on", "off". Precedence: --remote-docker flag >
// $TKN_ACT_REMOTE_DOCKER > "auto".
const envRemoteDocker = "TKN_ACT_REMOTE_DOCKER"

// resolveRemoteDocker normalizes the --remote-docker flag value
// against $TKN_ACT_REMOTE_DOCKER. Returns "auto", "on", or "off".
// Only "on" and "off" are decisive from the flag — "auto" (the flag
// default) falls through to env, then to "auto" when neither
// expresses a preference. Unknown values are ignored at each layer.
func resolveRemoteDocker(flagValue string) string {
	if v := decisiveRemoteDockerValue(flagValue); v != "" {
		return v
	}
	if v := decisiveRemoteDockerValue(os.Getenv(envRemoteDocker)); v != "" {
		return v
	}
	return "auto"
}

func decisiveRemoteDockerValue(v string) string {
	switch v {
	case "on", "off":
		return v
	}
	return ""
}
