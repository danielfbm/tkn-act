package main

import (
	"strings"
	"testing"
)

func TestResolveRemoteDocker(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		env     string
		want    string
		wantErr string
	}{
		{name: "flag on wins over env off", flag: "on", env: "off", want: "on"},
		{name: "flag off wins over env on", flag: "off", env: "on", want: "off"},
		{name: "flag auto falls back to env on", flag: "auto", env: "on", want: "on"},
		{name: "empty flag uses env off", flag: "", env: "off", want: "off"},
		{name: "env unrecognized falls back to auto", flag: "", env: "yes", want: "auto"},
		{name: "flag unrecognized is an error", flag: "yes", env: "off", wantErr: "invalid --remote-docker"},
		{name: "all empty -> auto", flag: "", env: "", want: "auto"},
		{name: "flag auto + env empty -> auto", flag: "auto", env: "", want: "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envRemoteDocker, tc.env)
			got, err := resolveRemoteDocker(tc.flag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substr %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveRemoteDocker(%q) with env=%q = %q, want %q",
					tc.flag, tc.env, got, tc.want)
			}
		})
	}
}
