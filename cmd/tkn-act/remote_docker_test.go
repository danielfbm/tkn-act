package main

import "testing"

func TestResolveRemoteDocker(t *testing.T) {
	cases := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{"flag on wins over env off", "on", "off", "on"},
		{"flag off wins over env on", "off", "on", "off"},
		{"flag auto falls back to env on", "auto", "on", "on"},
		{"empty flag uses env off", "", "off", "off"},
		{"env unrecognized falls back to auto", "", "yes", "auto"},
		{"flag unrecognized falls back to env", "yes", "off", "off"},
		{"all empty -> auto", "", "", "auto"},
		{"flag auto + env empty -> auto", "auto", "", "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envRemoteDocker, tc.env)
			if got := resolveRemoteDocker(tc.flag); got != tc.want {
				t.Errorf("resolveRemoteDocker(%q) with env=%q = %q, want %q",
					tc.flag, tc.env, got, tc.want)
			}
		})
	}
}
